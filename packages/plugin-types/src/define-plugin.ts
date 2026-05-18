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

/**
 * SSO provider implementation. Plugins that opt in to OpenNeko's auth
 * contract supply both handlers. The host (worker → web) drives the
 * OIDC dance: it calls `begin` to get an authorization URL,
 * redirects the browser there, then on callback calls `complete` to
 * trade the code for an identity assertion.
 */
export interface PluginAuthDefinition {
  /** Short label rendered on the sign-in button. */
  providerLabel?: string;
  begin: BeginAuthHandler;
  complete: CompleteAuthHandler;
}

export interface PluginDefinition {
  name: string;
  version: string;
  actions?: PluginActionDefinition[];
  /**
   * Optional SSO provider implementation. When set, the plugin's
   * manifest entry should also carry `provides_auth: true` so the
   * host can light up the "Sign in with …" UI without first spawning
   * the VM.
   */
  auth?: PluginAuthDefinition;
}

/**
 * Plugin entrypoint helper. Returns the same object passed in, but with
 * the type narrowed so editors give plugin authors completion on the
 * action shape. Authors do:
 *
 *     export default definePlugin({
 *       name: "@open-neko/plugin-parallel-search",
 *       version: "0.1.0",
 *       actions: [{
 *         kind: "web_search",
 *         description: "Search the web via Parallel.ai",
 *         handler: async (req) => { ... },
 *       }],
 *     });
 *
 * The plugin's runner script (provided by this package via `runPlugin`)
 * imports the default export and dispatches RPC calls to it.
 */
export function definePlugin(definition: PluginDefinition): PluginDefinition {
  if (!definition.name) {
    throw new Error("definePlugin: name is required");
  }
  if (!definition.version) {
    throw new Error("definePlugin: version is required");
  }
  for (const action of definition.actions ?? []) {
    if (typeof action.handler !== "function") {
      throw new Error(
        `definePlugin: action "${action.kind}" must provide a handler function`,
      );
    }
  }
  if (definition.auth) {
    if (typeof definition.auth.begin !== "function") {
      throw new Error("definePlugin: auth.begin must be a function");
    }
    if (typeof definition.auth.complete !== "function") {
      throw new Error("definePlugin: auth.complete must be a function");
    }
  }
  return definition;
}
