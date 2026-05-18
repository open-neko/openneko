import { z } from "zod";
import {
  PluginActionDeclaration,
  PluginActionOutcome,
  PluginActionRequest,
} from "./action.js";
import {
  BeginAuthParams,
  BeginAuthResult,
  CompleteAuthParams,
  CompleteAuthResult,
  PluginAuthDeclaration,
} from "./auth.js";

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

export const RpcMethod = z.enum([
  "register",
  "execute_action",
  "begin_auth",
  "complete_auth",
]);
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
  /**
   * Present when the plugin acts as an SSO identity provider. Absence
   * is normal — most plugins only contribute actions. The host pairs
   * this with the manifest's `provides_auth: true` flag: the flag is
   * what makes the auth flow visible in the UI; this declaration
   * supplies the human-readable label.
   */
  auth: PluginAuthDeclaration.optional(),
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

export const BeginAuthRpcParams = z.object({
  params: BeginAuthParams,
});
export type BeginAuthRpcParams = z.infer<typeof BeginAuthRpcParams>;

export const BeginAuthRpcResult = z.object({
  result: BeginAuthResult,
});
export type BeginAuthRpcResult = z.infer<typeof BeginAuthRpcResult>;

export const CompleteAuthRpcParams = z.object({
  params: CompleteAuthParams,
});
export type CompleteAuthRpcParams = z.infer<typeof CompleteAuthRpcParams>;

export const CompleteAuthRpcResult = z.object({
  result: CompleteAuthResult,
});
export type CompleteAuthRpcResult = z.infer<typeof CompleteAuthRpcResult>;

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
