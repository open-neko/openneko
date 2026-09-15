import { z } from "zod";
import { PluginActionDeclaration } from "./action.js";
import { ChannelCapabilityDeclaration } from "./channel.js";
import { DirectoryCapabilityDeclaration } from "./directory.js";
import {
  ConnectCredentialScope,
  ConnectFlow,
  ConnectScope,
} from "./connect.js";

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
 * reads that file and injects values into the plugin's OpenShell
 * sandbox at exec time.
 */
export const PluginEnvRequirement = z.object({
  key: EnvVarName,
  required: z.boolean().default(true),
  /** Hide the value at prompt + never echo it back. */
  secret: z.boolean().default(true),
  description: z.string().min(1).max(280),
  /**
   * How a secret reaches the plugin VM. "egress": held gateway-side and
   * substituted by the egress proxy onto outbound requests — the box sees only
   * a placeholder (for credentials sent to an external API, e.g. a bot token).
   * "box" (default): the value lives in the VM, for secrets the plugin compares
   * locally and never sends out (e.g. a webhook signing secret).
   */
  inject: z.enum(["egress", "box"]).optional(),
  /**
   * The host mints this value itself (48 random bytes, base64url) and
   * stores it in the secrets file — the operator is never prompted and
   * never needs to know it. For purely internal secrets like HMAC
   * signing keys. The CLI fills it at install; the worker backfills it
   * at registry load for plugins installed before the flag existed.
   * Rotation = delete the stored value and restart.
   */
  autogenerate: z.boolean().default(false),
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
  /**
   * How app users come to exist for this provider.
   *
   * "automatic" (default): the identity is attested by an external IdP,
   * so the core may create an app_user on first sign-in.
   *
   * "manual": the identity is self-asserted by the plugin (e.g. a
   * magic-link flow proves mailbox possession, nothing more). The core
   * MUST NOT auto-create users — sign-in succeeds only for users an
   * admin pre-provisioned, and unknown emails are silently dropped at
   * begin-auth so the flow can't be used to enumerate accounts.
   */
  provisioning: z.enum(["automatic", "manual"]).default("automatic"),
  /**
   * Whether begin_auth is unusable without a loginHint (e.g. magic
   * link needs the email to send anything). The sign-in page renders
   * the email field as required and the begin route rejects
   * hint-less requests before reaching the plugin.
   */
  loginHintRequired: z.boolean().default(false),
});

export type AuthCapabilityDeclaration = z.infer<typeof AuthCapabilityDeclaration>;

/**
 * Connect capability — per-operator OAuth/connector.
 *
 * Unlike `auth` (singleton SSO, one identity for the whole
 * deployment), `connect` is non-singleton: any number of installed
 * plugins can declare it, and each operator authorizes independently.
 * Credentials land in the per-operator section of the secrets store
 * (`_operators[operatorId][pluginName]` → ConnectorCredential).
 */
export const ConnectCapabilityDeclaration = z.object({
  /** Display label shown on the Connect button + status row. */
  providerLabel: z.string().min(1),
  /** OAuth scopes the connector will request at consent time. */
  scopes: z.array(ConnectScope).min(1),
  /**
   * Flow type the worker should drive. oauth2-pkce (default): the core
   * mints the code_verifier against a pre-registered client. mcp-oauth:
   * the plugin does MCP-OAuth-2.1 discovery + DCR and bridges begin →
   * complete via the oauthState echo.
   */
  flow: ConnectFlow.default("oauth2-pkce"),
  /**
   * "operator" (default): per-operator credentials. "deployment": one
   * org-wide credential consented by an admin, injected on every
   * action call and stored under a reserved secrets slot.
   */
  credentialScope: ConnectCredentialScope.default("operator"),
});

export type ConnectCapabilityDeclaration = z.infer<typeof ConnectCapabilityDeclaration>;

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
    connect: ConnectCapabilityDeclaration.optional(),
    channel: ChannelCapabilityDeclaration.optional(),
    directory: DirectoryCapabilityDeclaration.optional(),
  })
  .refine(
    (c) => c.action != null || c.auth != null || c.connect != null || c.channel != null || c.directory != null,
    {
      message:
        "capabilities must declare at least one surface (action, auth, connect, channel, directory)",
    },
  );

export type PluginCapabilitiesDeclaration = z.infer<
  typeof PluginCapabilitiesDeclaration
>;

/**
 * Snapshot of the install policy in effect when this entry was added.
 * Lets the worker registry flag entries whose source no longer matches
 * the current policy (grandfather, don't yank). Null on entries that
 * pre-date the install-policy gate.
 */
export const PluginManifestPolicySnapshot = z.object({
  allowUnverified: z.boolean(),
  allowGitUrlInstalls: z.boolean(),
  allowedMarketplaces: z.array(z.string()),
});

export type PluginManifestPolicySnapshot = z.infer<typeof PluginManifestPolicySnapshot>;

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
  /**
   * Where this entry came from. "marketplace" = via a trusted catalog;
   * "unverified" = bypassed marketplaces; "git-url" = community skill.
   * Absent on legacy entries pre-dating the install-policy gate.
   */
  installSource: z.enum(["marketplace", "unverified", "git-url"]).optional(),
  /** ISO timestamp the entry was added. Absent on legacy entries. */
  installedAt: z.string().optional(),
  /** Policy snapshot — see PluginManifestPolicySnapshot. */
  policySnapshot: PluginManifestPolicySnapshot.nullable().optional(),
});

export type PluginManifestEntry = z.infer<typeof PluginManifestEntry>;

export const PluginManifest = z.object({
  schema: z.literal("https://open-neko.github.io/plugins/manifest.schema.json"),
  plugins: z.array(PluginManifestEntry),
});

export type PluginManifest = z.infer<typeof PluginManifest>;

export const PLUGIN_MANIFEST_FILE = "openneko.plugins.json";
