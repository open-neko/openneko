import {
  BeginAuthRpcParams,
  BeginAuthRpcResult,
  BeginConnectRpcParams,
  BeginConnectRpcResult,
  CompleteAuthRpcParams,
  CompleteAuthRpcResult,
  CompleteConnectRpcParams,
  CompleteConnectRpcResult,
  ExecuteActionParams,
  ExecuteActionResult,
  RefreshConnectRpcParams,
  RefreshConnectRpcResult,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  RpcResponse,
  rpcErr,
  rpcOk,
} from "./rpc.js";
import {
  DeliverParams,
  DeliverResult,
  ParseInboundParams,
  ParseInboundResult,
  PollInboundParams,
  PollInboundResult,
  VerifyInboundParams,
  VerifyInboundResult,
} from "./channel.js";
import type { PluginDefinition } from "./define-plugin.js";

export interface RunPluginOptions {
  method: string;
  paramsJson: string;
}

/**
 * Single-shot RPC dispatcher used by plugin runner scripts. Reads the
 * method name + params JSON, dispatches to the plugin's declared
 * capability handlers, returns an RpcResponse. The plugin runner writes
 * the response as JSON to stdout for the worker to parse.
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
      case "begin_auth":
        return rpcOk(await runBeginAuth(plugin, options.paramsJson));
      case "complete_auth":
        return rpcOk(await runCompleteAuth(plugin, options.paramsJson));
      case "begin_connect":
        return rpcOk(await runBeginConnect(plugin, options.paramsJson));
      case "complete_connect":
        return rpcOk(await runCompleteConnect(plugin, options.paramsJson));
      case "refresh_connect":
        return rpcOk(await runRefreshConnect(plugin, options.paramsJson));
      case "deliver":
        return rpcOk(await runDeliver(plugin, options.paramsJson));
      case "parse_inbound":
        return rpcOk(await runParseInbound(plugin, options.paramsJson));
      case "verify_inbound":
        return rpcOk(await runVerifyInbound(plugin, options.paramsJson));
      case "poll_inbound":
        return rpcOk(await runPollInbound(plugin, options.paramsJson));
      default:
        return rpcErr("UNKNOWN_METHOD", `unknown RPC method: ${options.method}`);
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return rpcErr("PLUGIN_ERROR", message);
  }
}

function buildRegisterResult(plugin: PluginDefinition): RegisterResult {
  const caps = plugin.capabilities;
  return RegisterResult.parse({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: plugin.name,
    pluginVersion: plugin.version,
    capabilities: {
      action: caps.action
        ? {
            kinds: caps.action.kinds.map((a) => ({
              kind: a.kind,
              description: a.description,
            })),
          }
        : undefined,
      auth: caps.auth
        ? caps.auth.providerLabel
          ? { providerLabel: caps.auth.providerLabel }
          : {}
        : undefined,
      connect: caps.connect
        ? {
            providerLabel: caps.connect.providerLabel,
            scopes: caps.connect.scopes,
            flow: caps.connect.flow ?? "oauth2-pkce",
          }
        : undefined,
      channel: caps.channel
        ? {
            providerLabel: caps.channel.providerLabel,
            profile: caps.channel.profile,
            directions: caps.channel.directions,
            ingress: caps.channel.ingress ?? "none",
          }
        : undefined,
    },
  });
}

async function runExecuteAction(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<ExecuteActionResult> {
  const parsed = ExecuteActionParams.parse(JSON.parse(paramsJson));
  const action = plugin.capabilities.action?.kinds.find(
    (a) => a.kind === parsed.request.kind,
  );
  if (!action) {
    throw new Error(
      `plugin does not handle action kind "${parsed.request.kind}"`,
    );
  }
  const outcome = await action.handler(parsed.request);
  return ExecuteActionResult.parse({ outcome });
}

async function runBeginAuth(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<BeginAuthRpcResult> {
  const auth = plugin.capabilities.auth;
  if (!auth) {
    throw new Error("plugin does not implement an auth provider");
  }
  const parsed = BeginAuthRpcParams.parse(JSON.parse(paramsJson));
  const result = await auth.begin(parsed.params);
  return BeginAuthRpcResult.parse({ result });
}

async function runCompleteAuth(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<CompleteAuthRpcResult> {
  const auth = plugin.capabilities.auth;
  if (!auth) {
    throw new Error("plugin does not implement an auth provider");
  }
  const parsed = CompleteAuthRpcParams.parse(JSON.parse(paramsJson));
  const result = await auth.complete(parsed.params);
  return CompleteAuthRpcResult.parse({ result });
}

async function runBeginConnect(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<BeginConnectRpcResult> {
  const connect = plugin.capabilities.connect;
  if (!connect) {
    throw new Error("plugin does not implement a connect capability");
  }
  const parsed = BeginConnectRpcParams.parse(JSON.parse(paramsJson));
  const result = await connect.begin(parsed.params);
  return BeginConnectRpcResult.parse({ result });
}

async function runCompleteConnect(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<CompleteConnectRpcResult> {
  const connect = plugin.capabilities.connect;
  if (!connect) {
    throw new Error("plugin does not implement a connect capability");
  }
  const parsed = CompleteConnectRpcParams.parse(JSON.parse(paramsJson));
  const result = await connect.complete(parsed.params);
  return CompleteConnectRpcResult.parse({ result });
}

async function runRefreshConnect(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<RefreshConnectRpcResult> {
  const connect = plugin.capabilities.connect;
  if (!connect) {
    throw new Error("plugin does not implement a connect capability");
  }
  if (!connect.refresh) {
    throw new Error(
      "plugin's connect capability does not implement refresh — tokens won't be rotated",
    );
  }
  const parsed = RefreshConnectRpcParams.parse(JSON.parse(paramsJson));
  const result = await connect.refresh(parsed.params);
  return RefreshConnectRpcResult.parse({ result });
}

async function runDeliver(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<DeliverResult> {
  const channel = plugin.capabilities.channel;
  if (!channel) {
    throw new Error("plugin does not implement a channel");
  }
  const parsed = DeliverParams.parse(JSON.parse(paramsJson));
  return DeliverResult.parse(await channel.deliver(parsed));
}

async function runParseInbound(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<ParseInboundResult> {
  const channel = plugin.capabilities.channel;
  if (!channel?.parseInbound) {
    throw new Error("plugin's channel does not implement parse_inbound");
  }
  const parsed = ParseInboundParams.parse(JSON.parse(paramsJson));
  return ParseInboundResult.parse(await channel.parseInbound(parsed));
}

async function runVerifyInbound(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<VerifyInboundResult> {
  const channel = plugin.capabilities.channel;
  if (!channel?.verifyInbound) {
    throw new Error("plugin's channel does not implement verify_inbound");
  }
  const parsed = VerifyInboundParams.parse(JSON.parse(paramsJson));
  return VerifyInboundResult.parse(await channel.verifyInbound(parsed));
}

async function runPollInbound(
  plugin: PluginDefinition,
  paramsJson: string,
): Promise<PollInboundResult> {
  const channel = plugin.capabilities.channel;
  if (!channel?.pollInbound) {
    throw new Error("plugin's channel does not implement poll_inbound");
  }
  const parsed = PollInboundParams.parse(JSON.parse(paramsJson));
  return PollInboundResult.parse(await channel.pollInbound(parsed));
}

/**
 * Entrypoint for plugin runner scripts. Reads method + params from
 * argv, dispatches, prints RpcResponse JSON to stdout, exits. Authors
 * call this from their package's runner like:
 *
 *     // run.js
 *     import plugin from "./plugin.js";
 *     import { runPluginEntrypoint } from "@open-neko/plugin-types";
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
