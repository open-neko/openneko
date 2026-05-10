/**
 * Minimal JSON-RPC 2.0 client over a child process's stdio, scoped to the
 * subset of ACP (Agent Client Protocol) that Hermes' `acp` subcommand emits.
 * Frame schema captured from `hermes acp` v0.12.0 — notifications wrap their
 * payload under `params.update` (NOT `params` directly), and the discriminator
 * is `params.update.sessionUpdate`.
 */

import type { ChildProcess } from "node:child_process";
import { createInterface, type Interface } from "node:readline";

export type AcpToolCallStatus = "pending" | "in_progress" | "completed" | "failed";

export type AcpSessionUpdate =
  | {
      sessionUpdate: "agent_message_chunk";
      content: { type: "text"; text: string };
    }
  | {
      sessionUpdate: "agent_thought_chunk";
      content: { type: "text"; text: string };
    }
  | {
      sessionUpdate: "tool_call";
      toolCallId: string;
      title?: string;
      kind?: string;
      locations?: Array<{ path: string }>;
      rawInput?: unknown;
    }
  | {
      sessionUpdate: "tool_call_update";
      toolCallId: string;
      status?: AcpToolCallStatus;
      content?: unknown;
      rawOutput?: unknown;
    }
  | {
      sessionUpdate: "plan";
      entries: Array<{ content: string; status: string }>;
    }
  | { sessionUpdate: "available_commands_update"; availableCommands: unknown }
  | { sessionUpdate: "usage_update"; size: number; used: number };

export type AcpNotification = {
  method: "session/update";
  sessionId: string;
  update: AcpSessionUpdate;
};

export type AcpJsonRpcError = {
  code: number;
  message: string;
  data?: unknown;
};

export class AcpProtocolError extends Error {
  readonly code: number;
  constructor(err: AcpJsonRpcError) {
    super(err.message);
    this.name = "AcpProtocolError";
    this.code = err.code;
  }
}

type Pending = {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
};

export type AcpClient = {
  request: <T = unknown>(method: string, params?: unknown) => Promise<T>;
  onNotification: (handler: (notif: AcpNotification) => void) => void;
  dispose: () => void;
  closed: Promise<void>;
};

export function createAcpClient(child: ChildProcess): AcpClient {
  if (!child.stdout || !child.stdin) {
    throw new Error("createAcpClient: child process must have piped stdio");
  }
  const stdin = child.stdin;
  const rl: Interface = createInterface({ input: child.stdout, crlfDelay: Infinity });
  const pending = new Map<number, Pending>();
  const handlers: Array<(notif: AcpNotification) => void> = [];
  let nextId = 1;
  let closed = false;
  let closedResolve: () => void = () => {};
  const closedPromise = new Promise<void>((resolve) => {
    closedResolve = resolve;
  });

  const dispose = () => {
    if (closed) return;
    closed = true;
    rl.close();
    for (const p of pending.values()) {
      p.reject(new Error("ACP client disposed before response"));
    }
    pending.clear();
    closedResolve();
  };

  rl.on("line", (line) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    let frame: Record<string, unknown>;
    try {
      frame = JSON.parse(trimmed) as Record<string, unknown>;
    } catch {
      return;
    }
    if (typeof frame.id === "number") {
      const p = pending.get(frame.id);
      if (!p) return;
      pending.delete(frame.id);
      if (frame.error) {
        p.reject(new AcpProtocolError(frame.error as AcpJsonRpcError));
      } else {
        p.resolve(frame.result);
      }
      return;
    }
    if (typeof frame.method === "string" && frame.method === "session/update") {
      const params = (frame.params ?? {}) as {
        sessionId?: unknown;
        update?: unknown;
      };
      if (typeof params.sessionId === "string" && params.update && typeof params.update === "object") {
        const notif: AcpNotification = {
          method: "session/update",
          sessionId: params.sessionId,
          update: params.update as AcpSessionUpdate,
        };
        for (const h of handlers) {
          try {
            h(notif);
          } catch {
            // Don't let a handler kill the read loop.
          }
        }
      }
    }
  });

  rl.on("close", dispose);
  child.on("close", dispose);
  child.on("error", dispose);

  return {
    request<T = unknown>(method: string, params?: unknown): Promise<T> {
      if (closed) return Promise.reject(new Error("ACP client is closed"));
      const id = nextId++;
      return new Promise<T>((resolve, reject) => {
        pending.set(id, {
          resolve: (v) => resolve(v as T),
          reject,
        });
        const frame = JSON.stringify({ jsonrpc: "2.0", id, method, params: params ?? {} }) + "\n";
        try {
          stdin.write(frame);
        } catch (e) {
          pending.delete(id);
          reject(e instanceof Error ? e : new Error(String(e)));
        }
      });
    },
    onNotification(handler) {
      handlers.push(handler);
    },
    dispose,
    closed: closedPromise,
  };
}
