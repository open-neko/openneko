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
import { resolveTaskRoute, type ModelTask, type RoutedModel, type TaskRoute } from "./model-routes";
import { resolveAxModelRoute } from "./provider-runtime";
import { recordUsageSpend } from "./spend/ledger";
import { estimateRecordCost, estimateTokenCost } from "./spend/model-prices";

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
  fallback?: GatewayRoute;
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
  fallback?: boolean;
};

export type GatewayTransport = (request: GatewayRequest, key: string, fallbackKey?: string) => Promise<GatewayResult>;

export type GenerateResult<T> = {
  outputs: T;
  usage: AgentTokenUsage;
  provider: PrimaryProviderId;
  model: string;
  /** True when the task's fallback model answered. */
  fallback: boolean;
};

let transportOverride: GatewayTransport | undefined;

export function setGatewayTransport(transport: GatewayTransport | undefined): void {
  transportOverride = transport;
}

const CALL_TIMEOUT_MS = 10 * 60_000;

function viaServe(socket: string): GatewayTransport {
  return (request, key, fallbackKey) =>
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
        conn.write(`${JSON.stringify({ type: "generate", generate: request, model_key: key, ...(fallbackKey ? { fallback_key: fallbackKey } : {}) })}\n`);
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
  return (request, key, fallbackKey) =>
    new Promise<GatewayResult>((resolve, reject) => {
      const child = spawn(binary, ["generate"], {
        stdio: ["pipe", "pipe", "pipe"],
        env: {
          PATH: process.env.PATH ?? "",
          HARNESS_MODEL_API_KEY: key,
          ...(fallbackKey ? { HARNESS_FALLBACK_API_KEY: fallbackKey } : {}),
        } as unknown as NodeJS.ProcessEnv,
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

export function callGateway(request: GatewayRequest, key: string, fallbackKey?: string): Promise<GatewayResult> {
  return gatewayTransport()(request, key, fallbackKey);
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

function gatewayRoute(resolved: ResolvedPrimaryProviderConfig): GatewayRoute {
  const axRoute = resolveAxModelRoute({ provider: resolved.provider, model: resolved.model, config: resolved.config });
  return {
    provider: axRoute.provider,
    model: axRoute.model,
    ...(axRoute.url ? { url: axRoute.url } : {}),
    ...(axRoute.options ? { options: axRoute.options } : {}),
  };
}

/** The model record's price wins, then the catalog. A model with neither stays unpriced. */
export function priceHostUsage(usage: AgentTokenUsage, served: RoutedModel, route: GatewayRoute): AgentTokenUsage {
  if (usage.coverage === "unavailable") return { ...usage, costStatus: "unknown" };
  const tokens = {
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    cacheWriteTokens: usage.cacheWriteTokens,
    totalTokens: usage.totalTokens,
  };
  const cost = served.price ? estimateRecordCost(served.price, tokens) : estimateTokenCost(route.provider ?? "", route.model, tokens);
  if (!cost) return { ...usage, costStatus: "unknown" };
  return {
    ...usage,
    costStatus: cost.usd === 0 ? "included" : "estimated",
    estimatedCostUsd: cost.usd,
    currency: "USD",
    costSource: cost.costSource,
    pricingCatalogVersion: cost.pricingCatalogVersion,
  };
}

export async function generate<T extends Record<string, unknown> = Record<string, unknown>>(
  orgId: string | undefined,
  program: HostProgram,
  values: Record<string, unknown>,
  opts: { task: ModelTask; draft?: EditableProviderConfig },
): Promise<GenerateResult<T>> {
  const taskRoute: TaskRoute = orgId && !opts.draft
    ? await resolveTaskRoute(orgId, opts.task)
    : { task: "default", primary: { ref: "default", name: "default", config: await resolvePrimaryProviderConfig(orgId, opts.draft) } };
  return generateOnRoute<T>(orgId, taskRoute, program, values, opts.task);
}

async function generateOnRoute<T extends Record<string, unknown>>(
  orgId: string | undefined,
  taskRoute: TaskRoute,
  program: HostProgram,
  values: Record<string, unknown>,
  task?: ModelTask,
): Promise<GenerateResult<T>> {
  const { primary, fallback } = taskRoute;
  const route = gatewayRoute(primary.config);
  const fallbackRoute = fallback ? gatewayRoute(fallback.config) : undefined;
  const result = await callGateway(
    {
      route,
      ...(fallbackRoute ? { fallback: fallbackRoute } : {}),
      signature: program.signature,
      instruction: program.instruction,
      values,
    },
    await gatewayKey(primary.config),
    fallback ? await gatewayKey(fallback.config) : undefined,
  );
  const servedByFallback = Boolean(result.fallback && fallback && fallbackRoute);
  const served = servedByFallback ? fallback! : primary;
  const usage = priceHostUsage(gatewayUsage(result.usage), served, servedByFallback ? fallbackRoute! : route);
  if (orgId && task) {
    await recordUsageSpend({
      orgId,
      usage,
      source: "system",
      task,
      provider: served.config.provider,
      model: served.config.model,
      unpriced: "zero",
    }).catch((error) => console.warn(`[spend] host call not recorded: ${error instanceof Error ? error.message : error}`));
  }
  return {
    outputs: (result.outputs ?? {}) as T,
    usage,
    provider: served.config.provider,
    model: served.config.model,
    fallback: servedByFallback,
  };
}

const PROVIDER_TEST = hostProgram(`prompt:string -> reply:string "the word READY"`, "Reply with READY only.");

export async function testPrimaryProvider(
  orgId?: string,
  draft?: EditableProviderConfig,
): Promise<{ provider: string; model: string; source: ProviderSource }> {
  const resolved = await resolvePrimaryProviderConfig(orgId, draft);
  const result = await generateOnRoute<{ reply?: unknown }>(
    orgId,
    { task: "default", primary: { ref: "default", name: "default", config: resolved } },
    PROVIDER_TEST,
    { prompt: "Reply with READY only." },
  );
  if (!String(result.outputs.reply ?? "").trim()) {
    throw new Error("Provider test returned an empty response");
  }
  return { provider: resolved.provider, model: resolved.model, source: resolved.source };
}
