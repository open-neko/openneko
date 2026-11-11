// Protocol: apps/ax-harness/serve/serve.go. The key leaves this process only
// in the frame's model_key field, never in the request.

import { spawn } from "node:child_process";
import { createConnection } from "node:net";
import { createInterface } from "node:readline";
import type { AgentTokenUsage } from "./agent-backend";
import { serveSocket } from "./agent-backends/harness-turn";
import { AX_HARNESS_BINARY } from "./agent-runtime-contract";
import type { EditableProviderConfig, PrimaryProviderId } from "./config";
import { getGoogleToken, resolvePrimaryProviderConfig, type ProviderSource, type ResolvedPrimaryProviderConfig } from "./llm";
import { resolveAxModelRoute } from "./provider-runtime";

export type HostProgram = { signature: string; instruction: string };

/** The signature must be a static literal: apps/ax-harness/inference parses each one in a test. */
export function hostProgram(signature: string, instruction: string): HostProgram {
  return { signature, instruction };
}

export type GatewayRoute = {
  provider: string;
  model: string;
  url?: string;
  options?: Record<string, string>;
};

export type GatewayRequest = {
  route: GatewayRoute;
  signature: string;
  instruction: string;
  values: Record<string, unknown>;
};

type GatewayUsage = {
  requests: number;
  reported: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
  coverage: "complete" | "partial" | "unavailable";
};

export type GatewayResult = {
  outputs: Record<string, unknown>;
  usage: GatewayUsage;
  provider: string;
  model: string;
};

export type GatewayTransport = (request: GatewayRequest, key: string) => Promise<GatewayResult>;

export type GenerateResult<T> = {
  outputs: T;
  usage: AgentTokenUsage;
  provider: PrimaryProviderId;
  model: string;
};

let transportOverride: GatewayTransport | undefined;

export function setGatewayTransport(transport: GatewayTransport | undefined): void {
  transportOverride = transport;
}

const CALL_TIMEOUT_MS = 10 * 60_000;

function viaServe(socket: string): GatewayTransport {
  return (request, key) =>
    new Promise<GatewayResult>((resolve, reject) => {
      const conn = createConnection(socket);
      let result: GatewayResult | undefined;
      let settled = false;
      const finish = (error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        conn.destroy();
        if (error) reject(error);
        else if (result) resolve(result);
        else reject(new Error("openneko serve returned no model result"));
      };
      const timer = setTimeout(() => finish(new Error("model call timed out")), CALL_TIMEOUT_MS);
      conn.on("error", (error) => finish(new Error(`openneko serve unavailable: ${error.message}`)));
      conn.on("close", () => finish());
      conn.on("connect", () => {
        conn.write(`${JSON.stringify({ type: "generate", generate: request, model_key: key })}\n`);
      });
      const lines = createInterface({ input: conn });
      lines.on("line", (line) => {
        if (!line.trim()) return;
        let frame: { type?: string; result?: GatewayResult; code?: number; error?: string };
        try {
          frame = JSON.parse(line);
        } catch {
          finish(new Error("openneko serve wrote a line that is not JSON"));
          return;
        }
        if (frame.type === "result" && frame.result) result = frame.result;
        else if (frame.type === "exit") {
          finish(frame.code === 0 ? undefined : new Error(frame.error || `model call failed (code ${frame.code})`));
        }
      });
    });
}

function viaBinary(binary: string): GatewayTransport {
  return (request, key) =>
    new Promise<GatewayResult>((resolve, reject) => {
      const child = spawn(binary, ["generate"], {
        stdio: ["pipe", "pipe", "pipe"],
        env: { PATH: process.env.PATH ?? "", HARNESS_MODEL_API_KEY: key } as unknown as NodeJS.ProcessEnv,
        timeout: CALL_TIMEOUT_MS,
      });
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
      child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
      child.on("error", (error) => reject(new Error(`ax-harness unavailable: ${error.message}`)));
      child.on("close", (code) => {
        if (code !== 0) {
          reject(new Error(Buffer.concat(stderr).toString("utf8").trim().slice(-2000) || `model call failed (code ${code})`));
          return;
        }
        try {
          resolve(JSON.parse(Buffer.concat(stdout).toString("utf8")) as GatewayResult);
        } catch {
          reject(new Error("ax-harness returned a result that is not JSON"));
        }
      });
      child.stdin.on("error", () => {});
      child.stdin.end(JSON.stringify(request));
    });
}

function gatewayTransport(): GatewayTransport {
  if (transportOverride) return transportOverride;
  const socket = serveSocket();
  return socket ? viaServe(socket) : viaBinary(process.env.OPENNEKO_AX_HARNESS_BINARY || AX_HARNESS_BINARY);
}

export function callGateway(request: GatewayRequest, key: string): Promise<GatewayResult> {
  return gatewayTransport()(request, key);
}

async function gatewayKey(resolved: ResolvedPrimaryProviderConfig): Promise<string> {
  if (resolved.provider === "vertex") return getGoogleToken();
  return resolved.secrets.apiKey || "none";
}

export function gatewayUsage(usage: GatewayUsage | undefined): AgentTokenUsage {
  if (!usage || usage.coverage === "unavailable" || !usage.reported) {
    return { coverage: "unavailable", missingReasons: ["The model call returned no token usage"] };
  }
  return {
    inputTokens: usage.input_tokens,
    outputTokens: usage.output_tokens,
    totalTokens: usage.total_tokens,
    ...(usage.cache_read_tokens !== undefined ? { cacheReadTokens: usage.cache_read_tokens } : {}),
    ...(usage.cache_write_tokens !== undefined ? { cacheWriteTokens: usage.cache_write_tokens } : {}),
    ...(usage.reasoning_tokens !== undefined ? { reasoningTokens: usage.reasoning_tokens } : {}),
    coverage: usage.coverage,
    ...(usage.coverage === "partial" ? { missingReasons: ["Some model calls omitted token usage"] } : {}),
  };
}

export async function generate<T extends Record<string, unknown> = Record<string, unknown>>(
  orgId: string | undefined,
  program: HostProgram,
  values: Record<string, unknown>,
  opts: { draft?: EditableProviderConfig } = {},
): Promise<GenerateResult<T>> {
  const resolved = await resolvePrimaryProviderConfig(orgId, opts.draft);
  const axRoute = resolveAxModelRoute({ provider: resolved.provider, model: resolved.model, config: resolved.config });
  const route: GatewayRoute = {
    provider: axRoute.provider,
    model: axRoute.model,
    ...(axRoute.url ? { url: axRoute.url } : {}),
    ...(axRoute.options ? { options: axRoute.options } : {}),
  };
  const result = await callGateway(
    { route, signature: program.signature, instruction: program.instruction, values },
    await gatewayKey(resolved),
  );
  return {
    outputs: (result.outputs ?? {}) as T,
    usage: gatewayUsage(result.usage),
    provider: resolved.provider,
    model: resolved.model,
  };
}

const PROVIDER_TEST = hostProgram(`prompt:string -> reply:string "the word READY"`, "Reply with READY only.");

export async function testPrimaryProvider(
  orgId?: string,
  draft?: EditableProviderConfig,
): Promise<{ provider: string; model: string; source: ProviderSource }> {
  const resolved = await resolvePrimaryProviderConfig(orgId, draft);
  const result = await generate<{ reply?: unknown }>(orgId, PROVIDER_TEST, { prompt: "Reply with READY only." }, { draft });
  if (!String(result.outputs.reply ?? "").trim()) {
    throw new Error("Provider test returned an empty response");
  }
  return { provider: resolved.provider, model: resolved.model, source: resolved.source };
}
