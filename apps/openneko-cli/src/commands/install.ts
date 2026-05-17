// Thin CLI wrapper around runInstall from @open-neko/plugin-install.
// The CLI provides:
//   - the trusted-marketplaces list (read from its per-user
//     marketplace-store, which is CLI-only state)
//   - a TTY-aware env prompt that hides input by default
// Everything else (marketplace fetch, plugin pick, npm install,
// manifest write, secrets-store update) lives in the library so the
// worker can reuse it server-side when the agent's `install_plugin`
// action lands.
import {
  runInstall as libRunInstall,
  type InstallResult,
  type MarketplaceClient,
  type MarketplaceEnvRequirement,
  type ParsedSpec,
} from "@open-neko/plugin-install";
import { parseInstallSpec as libParseInstallSpec } from "@open-neko/plugin-install";
import { readStore } from "../marketplace-store.js";

export interface InstallOptions {
  repoRoot: string;
  /** Spec is `<pluginName>` or `<pluginName>@<marketplace-name-or-url>`. */
  spec: string;
  version?: string;
  unverified?: boolean;
  /** Override config dir for the trusted-marketplaces + secrets stores. */
  configDir?: string;
  marketplaceClient?: MarketplaceClient;
  /** For tests: skip the npm subprocess call. */
  npmRunner?: (args: string[], cwd: string) => Promise<void>;
  /**
   * Resolve the value of an env requirement during install. Tests
   * override; the CLI's default uses a TTY-aware hidden prompt.
   */
  envPrompt?: (
    plugin: string,
    requirement: MarketplaceEnvRequirement,
  ) => Promise<string>;
}

export type { InstallResult, ParsedSpec };

export function parseInstallSpec(spec: string): ParsedSpec {
  return libParseInstallSpec(spec);
}

export async function runInstall(
  options: InstallOptions,
): Promise<InstallResult> {
  const trustedMarketplaces = options.unverified
    ? []
    : (await readStore(options.configDir)).marketplaces.map((m) => ({
        name: m.name,
        url: m.url,
      }));

  return libRunInstall({
    repoRoot: options.repoRoot,
    spec: options.spec,
    ...(options.version !== undefined ? { version: options.version } : {}),
    ...(options.unverified !== undefined
      ? { unverified: options.unverified }
      : {}),
    trustedMarketplaces,
    ...(options.configDir !== undefined
      ? { secretsConfigDir: options.configDir }
      : {}),
    ...(options.marketplaceClient !== undefined
      ? { marketplaceClient: options.marketplaceClient }
      : {}),
    ...(options.npmRunner !== undefined ? { npmRunner: options.npmRunner } : {}),
    envPrompt: options.envPrompt ?? defaultEnvPrompt(),
  });
}

function defaultEnvPrompt() {
  return async (
    plugin: string,
    requirement: MarketplaceEnvRequirement,
  ): Promise<string> => {
    const { isInteractive, promptHidden, promptVisible } = await import(
      "../prompt.js"
    );
    if (!isInteractive()) {
      throw new Error(
        `install: required env "${requirement.key}" for ${plugin} is not set ` +
          `and stdin is not a TTY.\n` +
          `Run: openneko secrets set ${plugin} ${requirement.key} <value>`,
      );
    }
    const header =
      `\n${plugin} requires ${requirement.key}\n  ${requirement.description}\n`;
    process.stdout.write(header);
    const label = requirement.secret === false
      ? "value: "
      : `${requirement.key} (hidden): `;
    return requirement.secret === false
      ? promptVisible(label)
      : promptHidden(label);
  };
}
