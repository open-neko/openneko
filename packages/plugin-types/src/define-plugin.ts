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
 * What the plugin author returns from definePlugin. Mirror of
 * PluginCapabilitiesDeclaration but each present surface carries
 * handlers in addition to its declared metadata.
 */
export interface PluginCapabilitiesImpl {
  action?: ActionCapabilityImpl;
  auth?: AuthCapabilityImpl;
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
  if (!caps || (caps.action == null && caps.auth == null)) {
    throw new Error(
      "definePlugin: capabilities must declare at least one surface (action, auth)",
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
  return definition;
}
