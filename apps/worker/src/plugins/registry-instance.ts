// Process-wide PluginRegistry handle. The worker's startup constructs
// one PluginRegistry, stashes it here, and worker job handlers ask
// for it when they need the scrubber. Keeps job functions pure — they
// don't have to receive a registry through three layers of payload.
import { createScrubber, type Scrubber } from "@neko/llm/work";
import type { PluginRegistry } from "./plugin-registry.js";

let instance: PluginRegistry | null = null;

export function setPluginRegistryInstance(reg: PluginRegistry | null): void {
  instance = reg;
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
