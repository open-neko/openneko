// Process-wide PluginRegistry handle. The worker's startup constructs
// one PluginRegistry, stashes it here, and worker job handlers ask
// for it when they need the scrubber. Keeps job functions pure — they
// don't have to receive a registry through three layers of payload.
import { createScrubber, registerHarnessInstalledPluginAvailability, type Scrubber } from "@neko/llm/work";
import type { PluginRegistry } from "./plugin-registry.js";

let instance: PluginRegistry | null = null;
let unregisterAvailability: (() => void) | null = null;

export function setPluginRegistryInstance(reg: PluginRegistry | null): void {
  unregisterAvailability?.();
  unregisterAvailability = null;
  instance = reg;
  if (reg) {
    unregisterAvailability = registerHarnessInstalledPluginAvailability(grant => {
      const matches = reg.getRegisteredActionDescriptors().filter(action => action.kind === grant.kind);
      if (matches.length !== 1 || matches[0].pluginName !== grant.pluginName ||
          matches[0].pluginVersion !== grant.pluginVersion ||
          matches[0].pluginIntegrity !== grant.pluginIntegrity) return false;
      const mode = matches[0].default_mode;
      return (typeof mode === "object" ? mode[grant.scope] : mode) !== "deny";
    });
  }
}

export function getPluginRegistryInstance(): PluginRegistry | null {
  return instance;
}

/**
 * Convenience for jobs: return the current scrubber, or a no-op if
 * no registry has been installed (tests, or worker booting without
 * the plugin subsystem).
 */
export function getCurrentScrubber(): Scrubber {
  return instance?.getScrubber() ?? createScrubber([]);
}
