import {
  ExecuteActionParams,
  ExecuteActionResult,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  RpcResponse,
  rpcErr,
  rpcOk,
} from "./rpc.js";
import type { PluginDefinition } from "./define-plugin.js";

export interface RunPluginOptions {
  method: string;
  paramsJson: string;
}

/**
 * Single-shot RPC dispatcher used by plugin runner scripts. Reads the
 * method name + params JSON, dispatches to the plugin's declarations or
 * handlers, returns an RpcResponse. The plugin runner writes the
 * response as JSON to stdout for the worker to parse.
 *
 * v1 contract: one process per call. If/when a future microsandbox SDK
 * supports long-running stdio streams, the same dispatcher can be
 * driven in a loop reading newline-delimited requests.
 */
export async function dispatchPluginRpc(
  plugin: PluginDefinition,
  options: RunPluginOptions,
): Promise<RpcResponse> {
  try {
    switch (options.method) {
      case "register":
        return rpcOk(buildRegisterResult(plugin));
      case "execute_action":
        return rpcOk(await runExecuteAction(plugin, options.paramsJson));
      default:
        return rpcErr("UNKNOWN_METHOD", `unknown RPC method: ${options.method}`);
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return rpcErr("PLUGIN_ERROR", message);
  }
}

function buildRegisterResult(plugin: PluginDefinition): RegisterResult {
  return RegisterResult.parse({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: plugin.name,
    pluginVersion: plugin.version,
    actions: (plugin.actions ?? []).map((a) => ({
      kind: a.kind,
      description: a.description,
    })),
  });
}

async function runExecuteAction(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<ExecuteActionResult> {
  const parsed = ExecuteActionParams.parse(JSON.parse(paramsJson));
  const action = (plugin.actions ?? []).find(
    (a) => a.kind === parsed.request.kind,
  );
  if (!action) {
    throw new Error(`plugin does not handle action kind "${parsed.request.kind}"`);
  }
  const outcome = await action.handler(parsed.request);
  return ExecuteActionResult.parse({ outcome });
}

/**
 * Entrypoint for plugin runner scripts. Reads method + params from
 * argv, dispatches, prints RpcResponse JSON to stdout, exits. Authors
 * call this from their package's runner like:
 *
 *     // run.js
 *     import plugin from "./plugin.js";
 *     import { runPluginEntrypoint } from "@open-neko/plugin-types/runner";
 *     await runPluginEntrypoint(plugin);
 */
export async function runPluginEntrypoint(
  plugin: PluginDefinition,
): Promise<void> {
  const method = process.argv[2];
  const paramsJson = process.argv[3] ?? "{}";
  if (!method) {
    process.stdout.write(
      JSON.stringify(rpcErr("MISSING_METHOD", "method name argv[2] required")),
    );
    process.exit(2);
  }
  const response = await dispatchPluginRpc(plugin, { method, paramsJson });
  process.stdout.write(JSON.stringify(response));
  process.exit(response.ok ? 0 : 1);
}
