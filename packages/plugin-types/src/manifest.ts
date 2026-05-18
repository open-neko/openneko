import { z } from "zod";
import { PluginActionDeclaration } from "./action.js";

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
 * One env var a plugin needs at runtime. Listed under `permissions.env`
 * in the marketplace entry and installed manifest; the openneko CLI
 * prompts the operator for any required ones during `openneko install`
 * and stores values in ~/.config/openneko/secrets.json. The worker
 * reads that file and injects values into the plugin's microVM at exec
 * time.
 */
export const PluginEnvRequirement = z.object({
  key: EnvVarName,
  required: z.boolean().default(true),
  /** Hide the value at prompt + never echo it back. */
  secret: z.boolean().default(true),
  description: z.string().min(1).max(280),
});

export type PluginEnvRequirement = z.infer<typeof PluginEnvRequirement>;

/**
 * What the plugin needs from the runtime to operate.
 *
 * - `network`: hostnames the sandbox must allow outbound traffic to.
 *   Enforced at the VM boundary — the plugin cannot reach hosts not
 *   listed here.
 * - `env`: env vars the operator must supply. `openneko install`
 *   prompts and refuses to complete if a required value is missing.
 *
 * Same shape in the marketplace entry and the installed manifest.
 */
export const PluginPermissions = z
  .object({
    network: z.array(HostPattern).default([]),
    env: z.array(PluginEnvRequirement).default([]),
  })
  .default({ network: [], env: [] });

export type PluginPermissions = z.infer<typeof PluginPermissions>;

/**
 * Action capability — a plugin contributes one or more named action
 * handlers the agent can invoke. Each kind has a snake_case identifier
 * and a description the agent uses to pick the right one.
 */
export const ActionCapabilityDeclaration = z.object({
  kinds: z.array(PluginActionDeclaration).min(1),
});

export type ActionCapabilityDeclaration = z.infer<
  typeof ActionCapabilityDeclaration
>;

/**
 * Auth capability — a plugin acts as the SSO provider for OpenNeko.
 * Singleton: only one installed plugin may declare this. Presence of
 * `capabilities.auth` on a manifest entry is what lights up the
 * "Sign in with …" UI; no VM spawn needed to make that decision.
 */
export const AuthCapabilityDeclaration = z.object({
  /**
   * Short human-readable provider label rendered on the sign-in
   * button (e.g. "Scalekit", "Okta", "Keycloak"). The host falls back
   * to a name-derived label when this is absent.
   */
  providerLabel: z.string().min(1).optional(),
});

export type AuthCapabilityDeclaration = z.infer<typeof AuthCapabilityDeclaration>;

/**
 * The full capability map a plugin contributes. Each surface a plugin
 * implements becomes a key here; the keyset IS the declaration — there
 * are no parallel flags. To add a new surface: add a key, its
 * declaration schema, the matching impl, and the RPC dispatch.
 */
export const PluginCapabilitiesDeclaration = z
  .object({
    action: ActionCapabilityDeclaration.optional(),
    auth: AuthCapabilityDeclaration.optional(),
  })
  .refine((c) => c.action != null || c.auth != null, {
    message: "capabilities must declare at least one surface (action, auth)",
  });

export type PluginCapabilitiesDeclaration = z.infer<
  typeof PluginCapabilitiesDeclaration
>;

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
  permissions: PluginPermissions,
  capabilities: PluginCapabilitiesDeclaration,
  /**
   * Resolved env values for this plugin. The CLI normally writes
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
