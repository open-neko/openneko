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
import {
  BeginConnectParams,
  BeginConnectResult,
  CompleteConnectParams,
  CompleteConnectResult,
  RefreshConnectParams,
  RefreshConnectResult,
} from "./connect.js";
import type {
  CapabilityProfile,
  ChannelDirection,
  ChannelIngress,
  DeliverParams,
  DeliverResult,
  ParseInboundParams,
  ParseInboundResult,
  PollInboundParams,
  PollInboundResult,
  VerifyInboundParams,
  VerifyInboundResult,
} from "./channel.js";

export type PluginActionHandler = (
  request: PluginActionRequest,
) => Promise<PluginActionOutcome> | PluginActionOutcome;

export interface PluginActionDefinition extends PluginActionDeclaration {
  handler: PluginActionHandler;
}

export type BeginAuthHandler = (
  params: BeginAuthParams,
) => Promise<BeginAuthResult> | BeginAuthResult;

export type CompleteAuthHandler = (
  params: CompleteAuthParams,
) => Promise<CompleteAuthResult> | CompleteAuthResult;

export type BeginConnectHandler = (
  params: BeginConnectParams,
) => Promise<BeginConnectResult> | BeginConnectResult;

export type CompleteConnectHandler = (
  params: CompleteConnectParams,
) => Promise<CompleteConnectResult> | CompleteConnectResult;

export type RefreshConnectHandler = (
  params: RefreshConnectParams,
) => Promise<RefreshConnectResult> | RefreshConnectResult;

export type DeliverHandler = (
  params: DeliverParams,
) => Promise<DeliverResult> | DeliverResult;

export type ParseInboundHandler = (
  params: ParseInboundParams,
) => Promise<ParseInboundResult> | ParseInboundResult;

export type VerifyInboundHandler = (
  params: VerifyInboundParams,
) => Promise<VerifyInboundResult> | VerifyInboundResult;

export type PollInboundHandler = (
  params: PollInboundParams,
) => Promise<PollInboundResult> | PollInboundResult;

/** Implementation shape for the action capability — kinds with handlers. */
export interface ActionCapabilityImpl {
  kinds: PluginActionDefinition[];
}

/** Implementation shape for the auth capability — the OIDC begin/complete handlers. */
export interface AuthCapabilityImpl {
  providerLabel?: string;
  begin: BeginAuthHandler;
  complete: CompleteAuthHandler;
}

/**
 * Implementation shape for the connect capability — per-operator OAuth.
 *
 * `refresh` is optional: connectors whose tokens never need rotation
 * (rare — most OAuth providers expire access tokens after an hour) can
 * omit it. When omitted, the worker raises a clear error if any action
 * invocation looks like it needs a refresh.
 */
export interface ConnectCapabilityImpl {
  providerLabel: string;
  scopes: string[];
  flow?: "oauth2-pkce";
  begin: BeginConnectHandler;
  complete: CompleteConnectHandler;
  refresh?: RefreshConnectHandler;
}

/**
 * Implementation shape for the channel capability — a frontend.
 *
 * `deliver` projects InteractionEvents into the substrate's native payload and
 * sends them. `parseInbound` / `verifyInbound` are present only for channels
 * whose `directions` include "inbound"; the webhook secret used by
 * `verifyInbound` stays inside the VM.
 */
export interface ChannelCapabilityImpl {
  providerLabel: string;
  profile: CapabilityProfile;
  directions: ChannelDirection[];
  ingress?: ChannelIngress;
  deliver: DeliverHandler;
  parseInbound?: ParseInboundHandler;
  verifyInbound?: VerifyInboundHandler;
  // Pull transport for hosts without a public webhook URL. Optional: channels
  // that only support webhook ingress omit it.
  pollInbound?: PollInboundHandler;
}

/**
 * What the plugin author returns from definePlugin. Mirror of
 * PluginCapabilitiesDeclaration but each present surface carries
 * handlers in addition to its declared metadata.
 */
export interface PluginCapabilitiesImpl {
  action?: ActionCapabilityImpl;
  auth?: AuthCapabilityImpl;
  connect?: ConnectCapabilityImpl;
  channel?: ChannelCapabilityImpl;
}

export interface PluginDefinition {
  name: string;
  version: string;
  capabilities: PluginCapabilitiesImpl;
}

/**
 * Plugin entrypoint helper. Validates that `capabilities` declares at
 * least one surface and that each declared surface has its required
 * handlers, then returns the same object so editors give plugin
 * authors completion on the capability shape.
 *
 *     export default definePlugin({
 *       name: "@open-neko/plugin-parallel-search",
 *       version: "0.1.0",
 *       capabilities: {
 *         action: {
 *           kinds: [{
 *             kind: "web_search",
 *             description: "Search the web",
 *             handler: async (req) => { ... },
 *           }],
 *         },
 *       },
 *     });
 *
 * The plugin's runner script (provided by this package via
 * `runPluginEntrypoint`) imports the default export and dispatches RPC
 * calls to it.
 */
export function definePlugin(definition: PluginDefinition): PluginDefinition {
  if (!definition.name) {
    throw new Error("definePlugin: name is required");
  }
  if (!definition.version) {
    throw new Error("definePlugin: version is required");
  }
  const caps = definition.capabilities;
  if (
    !caps ||
    (caps.action == null && caps.auth == null && caps.connect == null && caps.channel == null)
  ) {
    throw new Error(
      "definePlugin: capabilities must declare at least one surface (action, auth, connect, channel)",
    );
  }
  if (caps.action) {
    if (!Array.isArray(caps.action.kinds) || caps.action.kinds.length === 0) {
      throw new Error(
        "definePlugin: capabilities.action.kinds must list at least one action",
      );
    }
    for (const a of caps.action.kinds) {
      if (typeof a.handler !== "function") {
        throw new Error(
          `definePlugin: action "${a.kind}" must provide a handler function`,
        );
      }
    }
  }
  if (caps.auth) {
    if (typeof caps.auth.begin !== "function") {
      throw new Error("definePlugin: capabilities.auth.begin must be a function");
    }
    if (typeof caps.auth.complete !== "function") {
      throw new Error(
        "definePlugin: capabilities.auth.complete must be a function",
      );
    }
  }
  if (caps.connect) {
    if (!caps.connect.providerLabel) {
      throw new Error("definePlugin: capabilities.connect.providerLabel is required");
    }
    if (!Array.isArray(caps.connect.scopes) || caps.connect.scopes.length === 0) {
      throw new Error(
        "definePlugin: capabilities.connect.scopes must list at least one scope",
      );
    }
    if (typeof caps.connect.begin !== "function") {
      throw new Error("definePlugin: capabilities.connect.begin must be a function");
    }
    if (typeof caps.connect.complete !== "function") {
      throw new Error(
        "definePlugin: capabilities.connect.complete must be a function",
      );
    }
    if (caps.connect.refresh != null && typeof caps.connect.refresh !== "function") {
      throw new Error(
        "definePlugin: capabilities.connect.refresh must be a function when present",
      );
    }
  }
  if (caps.channel) {
    if (!caps.channel.providerLabel) {
      throw new Error("definePlugin: capabilities.channel.providerLabel is required");
    }
    if (caps.channel.profile == null) {
      throw new Error("definePlugin: capabilities.channel.profile is required");
    }
    if (!Array.isArray(caps.channel.directions) || caps.channel.directions.length === 0) {
      throw new Error(
        "definePlugin: capabilities.channel.directions must list at least one direction",
      );
    }
    if (typeof caps.channel.deliver !== "function") {
      throw new Error("definePlugin: capabilities.channel.deliver must be a function");
    }
  }
  return definition;
}
