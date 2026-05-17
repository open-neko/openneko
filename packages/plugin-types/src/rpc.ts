import { z } from "zod";
import {
  PluginActionDeclaration,
  PluginActionOutcome,
  PluginActionRequest,
} from "./action.js";

/**
 * JSON-RPC over stdio between the OpenNeko worker (caller) and a plugin
 * process inside its microsandbox VM (callee). v1 uses one-shot exec per
 * call — the worker invokes `node /workspace/plugin/run.js <method>
 * <json-params>` and reads a single JSON response from stdout. This
 * matches microsandbox's one-shot `exec()` model without requiring
 * long-running stdio streaming.
 *
 * If/when we add long-running stdio in a future microsandbox SDK rev,
 * this same wire format applies — just framed by newlines per request.
 */

export const RPC_PROTOCOL_VERSION = 1 as const;

export const RpcMethod = z.enum(["register", "execute_action"]);
export type RpcMethod = z.infer<typeof RpcMethod>;

/** Initial handshake the worker sends before any other method. */
export const RpcHello = z.object({
  protocol: z.literal(RPC_PROTOCOL_VERSION),
  pluginName: z.string(),
  pluginVersion: z.string(),
});
export type RpcHello = z.infer<typeof RpcHello>;

/** Returned by the plugin's register() — the surface it contributes. */
export const RegisterResult = z.object({
  protocol: z.literal(RPC_PROTOCOL_VERSION),
  pluginName: z.string(),
  pluginVersion: z.string(),
  actions: z.array(PluginActionDeclaration).default([]),
});
export type RegisterResult = z.infer<typeof RegisterResult>;

export const ExecuteActionParams = z.object({
  request: PluginActionRequest,
});
export type ExecuteActionParams = z.infer<typeof ExecuteActionParams>;

export const ExecuteActionResult = z.object({
  outcome: PluginActionOutcome,
});
export type ExecuteActionResult = z.infer<typeof ExecuteActionResult>;

export const RpcOk = z.object({
  ok: z.literal(true),
  result: z.unknown(),
});

export const RpcErr = z.object({
  ok: z.literal(false),
  error: z.object({
    code: z.string(),
    message: z.string(),
  }),
});

export const RpcResponse = z.discriminatedUnion("ok", [RpcOk, RpcErr]);
export type RpcResponse = z.infer<typeof RpcResponse>;

export type RpcOk = z.infer<typeof RpcOk>;
export type RpcErr = z.infer<typeof RpcErr>;

export class PluginRpcError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message);
    this.name = "PluginRpcError";
  }
}

export function rpcOk(result: unknown): RpcOk {
  return { ok: true, result };
}

export function rpcErr(code: string, message: string): RpcErr {
  return { ok: false, error: { code, message } };
}
