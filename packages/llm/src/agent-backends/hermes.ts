import { spawn, type ChildProcess } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type AgentBackend,
  type AgentRunOptions,
  type AgentRunResult,
  type AgentSurfaceMessage,
} from "../agent-backend";
import { registerAgentCanceller } from "../agent-shutdown";
import { hermesHomeForOrg } from "../host-provision";

function killProcessGroup(child: ChildProcess, signal: NodeJS.Signals): void {
  if (!child.pid) return;
  try {
    process.kill(-child.pid, signal);
  } catch {
    // ESRCH if already exited
  }
}

const FENCE_RE = /^\s*```(?:json)?\s*([\s\S]*?)\s*```\s*$/;

export function parseJsonFromOutput(raw: string): unknown {
  const trimmed = raw.trim();
  const fenced = trimmed.match(FENCE_RE);
  const candidate = (fenced?.[1] ?? trimmed).trim();
  try {
    return JSON.parse(candidate);
  } catch {
    const first = candidate.indexOf("{");
    const last = candidate.lastIndexOf("}");
    if (first === -1 || last === -1 || last < first) {
      throw new Error(
        `hermes output not parseable as JSON (no object braces found): ${candidate.slice(0, 200)}`,
      );
    }
    return JSON.parse(candidate.slice(first, last + 1));
  }
}

const DEFAULT_TIMEOUT_MS = 5 * 60_000;

export class HermesBackend implements AgentBackend {
  readonly id = "hermes" as const;

  async run(opts: AgentRunOptions): Promise<AgentRunResult> {
    const {
      prompt,
      userMessage,
      timeoutMs = DEFAULT_TIMEOUT_MS,
      retries = 1,
      debug = false,
      tag,
      orgId,
      workspace,
      skills,
      signal,
      onEvent,
      backendState = {},
    } = opts;

    const fullPrompt = userMessage
      ? `${prompt}\n\nCurrent user message:\n${userMessage}`
      : prompt;

    const args = ["-z", fullPrompt];
    if (skills && skills.length > 0) args.push("--skills", skills.join(","));

    if (onEvent) {
      await onEvent({ type: "status", message: "Hermes is working…" });
    }

    const maxAttempts = onEvent ? 1 : retries + 1;
    let lastErr: Error | undefined;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      try {
        const rawText = await spawnOnce({
          args,
          timeoutMs,
          debug,
          tag,
          orgId,
          workspace,
          signal,
          onStatus: onEvent
            ? (message) => {
                void onEvent({ type: "status", message });
              }
            : undefined,
        });
        let finalText = rawText;
        if (onEvent) {
          const parsed = extractSurfaceMessages(rawText);
          finalText = parsed.text;
          if (parsed.messages.length > 0) {
            await onEvent({ type: "surface", messages: parsed.messages });
          }
          if (finalText) {
            await onEvent({ type: "message", role: "assistant", content: finalText });
          }
        }
        return {
          finalText,
          status: signal?.aborted ? "cancelled" : "completed",
          backendState,
        };
      } catch (e) {
        lastErr = e instanceof Error ? e : new Error(String(e));
        if (debug) {
          console.warn(
            `[hermes] attempt ${attempt + 1}/${maxAttempts} failed: ${lastErr.message}`,
          );
        }
      }
    }

    const message = lastErr?.message ?? "hermes: unknown failure";
    if (signal?.aborted) {
      return { finalText: "", status: "cancelled", backendState };
    }
    if (onEvent) {
      await onEvent({ type: "error", message });
    }
    return { finalText: "", status: "failed", backendState, error: message };
  }
}

type SpawnArgs = {
  args: string[];
  timeoutMs: number;
  debug: boolean;
  tag: string | undefined;
  orgId: string | undefined;
  workspace: AgentRunOptions["workspace"];
  signal: AbortSignal | undefined;
  onStatus?: (message: string) => void;
};

async function spawnOnce({
  args,
  timeoutMs,
  debug,
  tag,
  orgId,
  workspace,
  signal,
  onStatus,
}: SpawnArgs): Promise<string> {
  let cwd: string;
  let cleanupScratch: (() => Promise<void>) | undefined;
  if (workspace) {
    cwd = workspace.orgRoot;
  } else {
    const safeTag = tag?.replace(/[^a-zA-Z0-9_-]/g, "").slice(0, 64);
    cwd = safeTag
      ? await (async () => {
          const exact = join(tmpdir(), safeTag);
          try {
            await mkdir(exact);
            return exact;
          } catch (e) {
            if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e;
            return mkdtemp(join(tmpdir(), `${safeTag}-`));
          }
        })()
      : await mkdtemp(join(tmpdir(), "neko-hermes-"));
    cleanupScratch = () =>
      rm(cwd, { recursive: true, force: true }).catch(() => {});
  }

  const env: NodeJS.ProcessEnv = workspace
    ? {
        ...process.env,
        PATH: `${workspace.binRoot}:${process.env.PATH || ""}`,
      }
    : { ...process.env };
  if (orgId) {
    env.HERMES_HOME = hermesHomeForOrg(orgId);
  }

  return new Promise<string>((resolve, reject) => {
    const child = spawn("hermes", args, {
      stdio: ["ignore", "pipe", "pipe"],
      cwd,
      env,
      detached: true,
    });
    const unregister = registerAgentCanceller(() =>
      killProcessGroup(child, "SIGKILL"),
    );

    const onAbort = () => killProcessGroup(child, "SIGTERM");
    if (signal) {
      if (signal.aborted) onAbort();
      else signal.addEventListener("abort", onAbort, { once: true });
    }

    const stdoutChunks: Buffer[] = [];
    const stderrChunks: Buffer[] = [];
    let stderrLineBuffer = "";
    let timer: NodeJS.Timeout | null = null;
    let settled = false;

    const finish = () => {
      if (timer) clearTimeout(timer);
      if (signal) signal.removeEventListener("abort", onAbort);
      unregister();
      cleanupScratch?.();
    };
    const settleResolve = (out: string) => {
      if (settled) return;
      settled = true;
      finish();
      resolve(out);
    };
    const settleReject = (err: Error) => {
      if (settled) return;
      settled = true;
      finish();
      reject(err);
    };

    child.stdout.on("data", (c: Buffer) => stdoutChunks.push(c));
    child.stderr.on("data", (c: Buffer) => {
      stderrChunks.push(c);
      if (onStatus) {
        const { lines, rest } = consumeStatusLines(stderrLineBuffer + c.toString("utf8"));
        stderrLineBuffer = rest;
        for (const line of lines) {
          onStatus(line);
        }
      }
      if (debug) process.stderr.write(c);
    });

    child.on("error", (e) => {
      settleReject(new Error(`hermes spawn failed: ${e.message}`));
    });

    child.on("close", (code) => {
      const stdout = Buffer.concat(stdoutChunks).toString("utf8");
      const stderr = Buffer.concat(stderrChunks).toString("utf8");
      if (onStatus && stderrLineBuffer.trim()) {
        onStatus(cleanStatusLine(stderrLineBuffer));
        stderrLineBuffer = "";
      }
      if (code !== 0) {
        settleReject(
          new Error(`hermes exited ${code}: ${stderr.slice(-500) || "(no stderr)"}`),
        );
        return;
      }
      settleResolve(stdout.trim());
    });

    timer = setTimeout(() => {
      killProcessGroup(child, "SIGTERM");
      settleReject(new Error(`hermes timed out after ${timeoutMs}ms`));
    }, timeoutMs);
  });
}

function consumeStatusLines(raw: string): { lines: string[]; rest: string } {
  const parts = raw.split(/\r?\n/);
  const rest = parts.pop() ?? "";
  const lines = parts
    .map(cleanStatusLine)
    .filter(Boolean)
    .slice(-5);
  return { lines, rest };
}

function cleanStatusLine(raw: string): string {
  return raw.replace(/\[[0-9;]*m/g, "").trim();
}

const NEKO_A2UI_FENCE_RE = /```neko_a2ui\s*([\s\S]*?)```/i;

export function extractSurfaceMessages(raw: string): {
  text: string;
  messages: AgentSurfaceMessage[];
} {
  const match = raw.match(NEKO_A2UI_FENCE_RE);
  if (!match) return { text: raw.trim(), messages: [] };
  try {
    const parsed = JSON.parse(match[1].trim());
    const messages = Array.isArray(parsed) ? (parsed as AgentSurfaceMessage[]) : [];
    const text = raw.replace(match[0], "").trim();
    return { text, messages };
  } catch {
    return { text: raw.trim(), messages: [] };
  }
}
