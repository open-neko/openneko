import {
  PluginActionDeclaration,
  PluginActionOutcome,
  PluginActionRequest,
} from "./action.js";

export type PluginActionHandler = (
  request: PluginActionRequest,
) => Promise<PluginActionOutcome> | PluginActionOutcome;

export interface PluginActionDefinition extends PluginActionDeclaration {
  handler: PluginActionHandler;
}

export interface PluginDefinition {
  name: string;
  version: string;
  actions?: PluginActionDefinition[];
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
  return definition;
}
