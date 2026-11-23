// One Ax turn as a stream of harness event lines, run by `openneko serve`
// over its socket. Serve protocol: apps/ax-harness/serve/serve.go.

import { createConnection } from "node:net";
import { createInterface } from "node:readline";
import type { Transport } from "@modelcontextprotocol/sdk/shared/transport.js";
import type { JSONRPCMessage } from "@modelcontextprotocol/sdk/types.js";

export type HarnessTurn = {
  /** The harness's NDJSON event lines, ending when the turn ends. */
  lines: AsyncIterable<string>;
  /** The harness's exit code; null when it was killed. */
  exited: Promise<number | null>;
  /** The end of the harness's diagnostic output. */
  diagnostics(): string;
  /** Asks the turn to stop; hard kills it when `hard`. */
  stop(hard?: boolean): void;
  readonly running: boolean;
};

/** Serves the run's OpenNeko tools over a transport, for a serve turn. */
export type ServeToolBridge = (
  servers: string[],
  env: Record<string, string>,
) => Promise<{ connect(transport: Transport): Promise<void>; close?(): Promise<void> }>;

let serveToolBridge: ServeToolBridge | undefined;

/** The worker registers how it serves tools to turns that run in serve. */
export function registerServeToolBridge(bridge: ServeToolBridge | undefined): void {
  serveToolBridge = bridge;
}

export function serveSocket(): string | undefined {
  return process.env.OPENNEKO_SERVE_SOCKET || undefined;
}

/** Variables a serve turn may carry. Everything else, keys above all, stays here. */
const SERVE_ENV = /^(OPENNEKO_MCP_(ORG_ID|THREAD_ID|SKILLS_ROOT)|OPENNEKO_HARNESS_[A-Z_]+|HARNESS_MODEL(_PROVIDER|_URL|_OPTIONS)?|HARNESS_MODEL_ROUTES|RUN_DIR|ARTIFACT_DIR|SKILLS_DIR|PYTHONPATH)$/;

/** Runs the turn in `openneko serve`. The model key travels in its own field. */
export function serveHarnessTurn(opts: {
  socket: string;
  spec: unknown;
  env: NodeJS.ProcessEnv;
  modelKeyEnv: string;
  modelKey: string;
  /** More route keys by variable name, for a turn with a fallback model. */
  extraKeys?: Record<string, string>;
  servers: string[];
  bridgeEnv?: Record<string, string>;
  debug?: boolean;
}): HarnessTurn {
  const conn = createConnection(opts.socket);
  const environ = Object.entries(opts.env)
    .filter((entry): entry is [string, string] => typeof entry[1] === "string" && SERVE_ENV.test(entry[0]))
    .map(([name, value]) => `${name}=${value}`);
  const bridge = Boolean(serveToolBridge && opts.bridgeEnv && opts.servers.length > 0);
  let diagnostics = "";
  let open = true;
  let resolveExit!: (code: number | null) => void;
  let rejectExit!: (error: Error) => void;
  const exited = new Promise<number | null>((resolve, reject) => {
    resolveExit = resolve;
    rejectExit = reject;
  });
  let exitCode: number | null = null;
  let exitSeen = false;
  const send = (frame: Record<string, unknown>): void => {
    if (open) conn.write(`${JSON.stringify(frame)}\n`);
  };

  // The harness's tool client talks to the worker's tool server in mcp frames.
  let transport: (Transport & { deliver(message: JSONRPCMessage): void; end(): void }) | undefined;
  let closeBridge: (() => Promise<void>) | undefined;
  if (bridge) {
    transport = {
      start: async () => {},
      send: async (message) => send({ type: "mcp", message }),
      close: async () => transport?.onclose?.(),
      deliver(message) {
        this.onmessage?.(message);
      },
      end() {
        this.onclose?.();
      },
    };
  }

  conn.on("error", (error) => {
    if (!exitSeen) rejectExit(new Error(`openneko serve unavailable: ${error.message}`));
  });
  conn.on("connect", () => {
    void (async () => {
      if (transport) {
        try {
          const server = await serveToolBridge!(opts.servers, opts.bridgeEnv!);
          await server.connect(transport);
          closeBridge = server.close?.bind(server);
        } catch (error) {
          diagnostics += `tool bridge failed: ${error instanceof Error ? error.message : String(error)}\n`;
          transport = undefined;
        }
      }
      send({
        type: "start",
        environ,
        spec: opts.spec,
        bridge: Boolean(transport),
        model_key_env: opts.modelKeyEnv,
        model_keys: { ...(opts.modelKey ? { [opts.modelKeyEnv]: opts.modelKey } : {}), ...(opts.extraKeys ?? {}) },
      });
    })();
  });

  async function* lines(): AsyncGenerator<string> {
    const reader = createInterface({ input: conn });
    try {
      for await (const line of reader) {
        if (!line.trim()) continue;
        let frame: { type?: string; event?: unknown; message?: JSONRPCMessage; code?: number; error?: string };
        try {
          frame = JSON.parse(line);
        } catch {
          diagnostics += "serve wrote a line that is not JSON\n";
          continue;
        }
        if (frame.type === "event") yield JSON.stringify(frame.event);
        else if (frame.type === "mcp" && frame.message) transport?.deliver(frame.message);
        else if (frame.type === "exit") {
          exitSeen = true;
          exitCode = typeof frame.code === "number" ? frame.code : null;
          if (frame.error) diagnostics += `${frame.error}\n`;
          break;
        }
      }
    } finally {
      open = false;
      conn.destroy();
      transport?.end();
      await closeBridge?.().catch(() => {});
      resolveExit(exitCode);
    }
  }

  if (opts.debug) conn.on("close", () => diagnostics && process.stderr.write(diagnostics));
  return {
    lines: lines(),
    exited,
    diagnostics: () => diagnostics,
    stop: () => {
      // Hanging up cancels the turn in serve.
      open = false;
      conn.destroy();
    },
    get running() {
      return open && !exitSeen;
    },
  };
}
