import { EventEmitter } from "node:events";
import { PassThrough, Writable } from "node:stream";

export type SpawnCall = {
  command: string;
  args: string[];
  options: { cwd?: string; env?: NodeJS.ProcessEnv; detached?: boolean };
};

export type AcpScript = {
  // Map from JSON-RPC method → response factory. The factory may also push
  // notifications via the supplied helper before returning the result frame.
  // Returning `undefined` means: do not respond (simulates a hang). Throwing
  // an Error becomes a JSON-RPC error response.
  responders?: Record<
    string,
    (
      params: unknown,
      ctx: { id: number; sessionId?: string; emitNotification: (n: unknown) => void },
    ) => unknown | Promise<unknown> | undefined | typeof NO_RESPONSE
  >;
  // Notifications keyed by method, emitted right before the response.
  notificationsByMethod?: Record<string, Array<unknown>>;
  // Lines written to stderr at spawn time (for status-line tests).
  stderrLines?: string[];
  // If true, the mock child will not auto-close on dispose; tests must close it.
  staysOpen?: boolean;
  // If set, the close event fires with this exit code instead of 0.
  exitCode?: number;
};

class LineRecordingWritable extends Writable {
  buffer = "";
  onLine: (line: string) => void = () => {};
  override _write(chunk: Buffer | string, _enc: string, cb: () => void): void {
    const text = typeof chunk === "string" ? chunk : chunk.toString("utf8");
    this.buffer += text;
    let idx: number;
    while ((idx = this.buffer.indexOf("\n")) !== -1) {
      const line = this.buffer.slice(0, idx);
      this.buffer = this.buffer.slice(idx + 1);
      try {
        this.onLine(line);
      } catch {
        // swallow handler errors
      }
    }
    cb();
  }
}

export type MockChild = EventEmitter & {
  pid: number;
  stdout: PassThrough;
  stderr: PassThrough;
  stdin: LineRecordingWritable;
  exitCode: number | null;
  kill: (signal?: NodeJS.Signals) => boolean;
};

export type MockHermesController = {
  spawnCalls: SpawnCall[];
  setScript: (script: AcpScript) => void;
  current: () => MockChild | undefined;
};

export function createMockSpawn(controller: MockHermesController) {
  return (
    command: string,
    args: string[],
    options: { cwd?: string; env?: NodeJS.ProcessEnv; detached?: boolean },
  ) => {
    controller.spawnCalls.push({ command, args, options });

    const script: AcpScript = (controller as { _script?: AcpScript })._script ?? {};
    const stdout = new PassThrough();
    const stderr = new PassThrough();
    const stdin = new LineRecordingWritable();
    const child = Object.assign(new EventEmitter(), {
      pid: 12345,
      stdout,
      stderr,
      stdin,
      exitCode: null as number | null,
      kill(signal?: NodeJS.Signals) {
        if (child.exitCode != null) return true;
        child.exitCode = signal === "SIGTERM" ? 143 : signal === "SIGKILL" ? 137 : 0;
        setImmediate(() => {
          stdout.end();
          stderr.end();
          child.emit("close", child.exitCode);
        });
        return true;
      },
    }) as MockChild;
    (controller as { _current?: MockChild })._current = child;

    setImmediate(() => {
      for (const line of script.stderrLines ?? []) {
        stderr.write(line + "\n");
      }
    });

    let nextSessionId: string | undefined;
    stdin.onLine = (line) => {
      const trimmed = line.trim();
      if (!trimmed) return;
      let frame: { id?: number; method?: string; params?: unknown };
      try {
        frame = JSON.parse(trimmed);
      } catch {
        return;
      }
      const { id, method, params } = frame;
      if (typeof id !== "number" || typeof method !== "string") return;

      const responder = script.responders?.[method];

      const emitNotification = (notif: unknown) => {
        const data = typeof notif === "string" ? notif : JSON.stringify(notif);
        stdout.write(data + "\n");
      };

      const inflight = (script.notificationsByMethod ?? {})[method] ?? [];
      for (const n of inflight) emitNotification(n);

      if (!responder) {
        const result = defaultResult(method, params, nextSessionId);
        if (method === "session/new" || method === "session/load") {
          nextSessionId = (result as { sessionId?: string }).sessionId;
        }
        stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
        if (method === "session/prompt" && !script.staysOpen) {
          setImmediate(() => {
            child.exitCode = script.exitCode ?? 0;
            child.emit("close", child.exitCode);
          });
        }
        return;
      }

      Promise.resolve()
        .then(() =>
          responder(params, { id, sessionId: nextSessionId, emitNotification }),
        )
        .then((handlerResult) => {
          // Sentinel `NO_RESPONSE` means hang (test simulates a stuck server).
          // Anything else, including `undefined`, falls through to the default
          // response so tests can use `cap.record(...)` without manually
          // composing the JSON-RPC reply.
          if (handlerResult === NO_RESPONSE) return;
          const result = handlerResult ?? defaultResult(method, params, nextSessionId);
          if (method === "session/new" || method === "session/load") {
            nextSessionId = (result as { sessionId?: string }).sessionId;
          }
          stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
          if (method === "session/prompt" && !script.staysOpen) {
            setImmediate(() => {
              child.exitCode = script.exitCode ?? 0;
              stdout.end();
              stderr.end();
              child.emit("close", child.exitCode);
            });
          }
        })
        .catch((err: Error & { code?: number }) => {
          stdout.write(
            JSON.stringify({
              jsonrpc: "2.0",
              id,
              error: { code: err.code ?? -32603, message: err.message },
            }) + "\n",
          );
          if (method === "session/prompt" && !script.staysOpen) {
            setImmediate(() => {
              child.exitCode = script.exitCode ?? 0;
              stdout.end();
              stderr.end();
              child.emit("close", child.exitCode);
            });
          }
        });
    };

    return child as unknown as ReturnType<typeof spawnReturnType>;
  };
}

export const NO_RESPONSE = Symbol("acp-no-response");

function defaultResult(method: string, _params: unknown, currentSessionId: string | undefined): unknown {
  switch (method) {
    case "initialize":
      return {
        agentCapabilities: { loadSession: true, promptCapabilities: { image: true } },
        agentInfo: { name: "hermes-agent", version: "0.0.0-test" },
        authMethods: [],
        protocolVersion: 1,
      };
    case "session/new":
      return { sessionId: `mock-session-${Math.random().toString(36).slice(2, 10)}` };
    case "session/load":
      return { sessionId: currentSessionId ?? "loaded-session" };
    case "session/prompt":
      return { stopReason: "end_turn", usage: { totalTokens: 0 } };
    default:
      return null;
  }
}

// Type-only placeholder so the cast above works without importing child_process here.
function spawnReturnType(): unknown {
  return undefined;
}

export function makeController(): MockHermesController {
  const c: MockHermesController = {
    spawnCalls: [],
    setScript(script) {
      (c as { _script?: AcpScript })._script = script;
    },
    current() {
      return (c as { _current?: MockChild })._current;
    },
  };
  return c;
}

export function chunkNotification(sessionId: string, text: string) {
  return {
    jsonrpc: "2.0",
    method: "session/update",
    params: {
      sessionId,
      update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text } },
    },
  };
}

export function toolCallNotification(sessionId: string, toolCallId: string, opts: { kind?: string; title?: string; locations?: Array<{ path: string }> } = {}) {
  return {
    jsonrpc: "2.0",
    method: "session/update",
    params: {
      sessionId,
      update: {
        sessionUpdate: "tool_call",
        toolCallId,
        kind: opts.kind ?? "read",
        title: opts.title ?? `read: ${toolCallId}`,
        locations: opts.locations ?? [],
      },
    },
  };
}

export function toolCallUpdateNotification(
  sessionId: string,
  toolCallId: string,
  opts: { status?: "in_progress" | "completed" | "failed"; rawOutput?: unknown; content?: unknown } = {},
) {
  return {
    jsonrpc: "2.0",
    method: "session/update",
    params: {
      sessionId,
      update: {
        sessionUpdate: "tool_call_update",
        toolCallId,
        status: opts.status ?? "completed",
        ...(opts.rawOutput !== undefined ? { rawOutput: opts.rawOutput } : {}),
        ...(opts.content !== undefined ? { content: opts.content } : {}),
      },
    },
  };
}
