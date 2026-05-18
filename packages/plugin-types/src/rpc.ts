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
} from "./auth.js";
import { AuthCapabilityDeclaration } from "./manifest.js";

/**
 * JSON-RPC over stdio between the OpenNeko worker (caller) and a plugin
 * process inside its microsandbox VM (callee). v1 uses one-shot exec per
 * call — the worker invokes `node /workspace/plugin/run.js <method>
 * <json-params>` and reads a single JSON response from stdout.
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

/**
 * Returned by the plugin's register() — the live-from-code capability
 * map. The worker validates this against the manifest's declared
 * capabilities; mismatched name, version, or surfaces refuse the VM.
 */
export const RegisterResult = z.object({
  protocol: z.literal(RPC_PROTOCOL_VERSION),
  pluginName: z.string(),
  pluginVersion: z.string(),
  capabilities: z.object({
    action: z
      .object({
        kinds: z.array(PluginActionDeclaration),
      })
      .optional(),
    auth: AuthCapabilityDeclaration.optional(),
  }),
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

export const BeginAuthRpcParams = z.object({ params: BeginAuthParams });
export type BeginAuthRpcParams = z.infer<typeof BeginAuthRpcParams>;

export const BeginAuthRpcResult = z.object({ result: BeginAuthResult });
export type BeginAuthRpcResult = z.infer<typeof BeginAuthRpcResult>;

export const CompleteAuthRpcParams = z.object({ params: CompleteAuthParams });
export type CompleteAuthRpcParams = z.infer<typeof CompleteAuthRpcParams>;

export const CompleteAuthRpcResult = z.object({ result: CompleteAuthResult });
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
