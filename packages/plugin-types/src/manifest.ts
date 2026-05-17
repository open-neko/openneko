import { z } from "zod";

export const HostPattern = z
  .string()
  .min(1)
  .max(253)
  .regex(
    /^[a-z0-9*]([a-z0-9-.*])*[a-z0-9*]$/i,
    "host pattern must be a hostname (subdomains allowed via leading *.)",
  );

export const EnvVarName = z
  .string()
  .min(1)
  .max(64)
  .regex(
    /^[A-Z][A-Z0-9_]*$/,
    "env var names must be UPPER_SNAKE_CASE starting with a letter",
  );

/**
 * One env var a plugin needs at runtime. Listed in the marketplace
 * entry; the openneko CLI prompts the operator for any required ones
 * during `openneko install` and stores them in the per-user secrets
 * file at ~/.config/openneko/secrets.json. The worker reads that file
 * and injects the values into the plugin's microVM at exec time.
 */
export const PluginEnvRequirement = z.object({
  key: EnvVarName,
  required: z.boolean().default(true),
  /** Hide the value at prompt + never echo it back. */
  secret: z.boolean().default(true),
  description: z.string().min(1).max(280),
});

export type PluginEnvRequirement = z.infer<typeof PluginEnvRequirement>;

export const PluginCapabilities = z
  .object({
    network: z.array(HostPattern).default([]),
  })
  .default({ network: [] });

export type PluginCapabilities = z.infer<typeof PluginCapabilities>;

export const ActionKindName = z
  .string()
  .min(1)
  .regex(
    /^[a-z][a-z0-9_]*$/,
    "action kind must be lowercase snake_case",
  );

export const PluginManifestEntry = z.object({
  name: z
    .string()
    .min(3)
    .regex(
      /^(@[a-z0-9-]+\/)?[a-z0-9][a-z0-9-_]*$/,
      "package name must be a valid npm package name",
    ),
  version: z
    .string()
    .regex(
      /^\d+\.\d+\.\d+(-[0-9A-Za-z-.]+)?$/,
      "version must be a pinned semver (no ranges)",
    ),
  integrity: z
    .string()
    .regex(/^sha512-[A-Za-z0-9+/=]+$/, "integrity must be sha512-<base64>"),
  capabilities: PluginCapabilities,
  /**
   * Action kinds this plugin handles — copied at install time from
   * the marketplace entry's `kinds`. Lets the worker build a kind →
   * plugin map from the file alone, without spawning a VM to call
   * register(). Optional so older manifests written before this
   * field existed still parse; the worker treats a missing list as
   * "discover via register() at boot" (legacy fallback).
   */
  kinds: z.array(ActionKindName).optional(),
  /**
   * Inline env values to set for this plugin. The CLI normally writes
   * secrets to the gitignored per-user store at ~/.config/openneko/
   * secrets.json; this field is for tests + non-secret defaults. The
   * worker merges both, with the per-user store winning.
   */
  env: z.record(z.string(), z.string()).optional(),
  /** Display name of the marketplace this plugin came from (traceability). */
  marketplace: z.string().optional(),
});

export type PluginManifestEntry = z.infer<typeof PluginManifestEntry>;

export const PluginManifest = z.object({
  schema: z.literal("https://open-neko.github.io/plugins/manifest.schema.json"),
  plugins: z.array(PluginManifestEntry),
});

export type PluginManifest = z.infer<typeof PluginManifest>;

export const PLUGIN_MANIFEST_FILE = "openneko.plugins.json";
