import { z } from "zod";

/**
 * Plugin-as-SSO-provider contract.
 *
 * OpenNeko's core stays identity-vendor-neutral by delegating the OIDC
 * code-exchange to any installed plugin whose manifest declares
 * `provides_auth: true`. The contract is intentionally generic: a
 * begin-auth call yields a provider URL + state, and a complete-auth
 * call exchanges the authorization code for an identity assertion.
 *
 * Plugins implement these two methods and the core gets a pluggable
 * "Sign in with <provider>" flow — Scalekit (which fronts Okta /
 * Entra / Google Workspace / etc.), a direct Okta plugin, a
 * self-hosted Keycloak plugin, all fit the same shape.
 */

export const AuthIdentity = z.object({
  /** Stable subject identifier from the IdP. Treated as the primary key. */
  sub: z.string().min(1),
  email: z.string().min(3),
  /** Display name. May be absent for IdPs that don't return one. */
  name: z.string().nullable().optional(),
  /**
   * IdP-supplied tenant identifier. Scalekit returns its organization id
   * here; raw OIDC plugins typically map this to the `org_id` claim.
   * Optional because not every IdP supplies it.
   */
  orgId: z.string().nullable().optional(),
  /**
   * Group / role memberships from the IdP, used by OpenNeko's role
   * mapping. Empty list means the IdP returned no groups — the core
   * falls back to a default role.
   */
  groups: z.array(z.string()).default([]),
});

export type AuthIdentity = z.infer<typeof AuthIdentity>;

export const BeginAuthParams = z.object({
  /**
   * Where the IdP should send the user after the code is issued. Must
   * be on OpenNeko itself (e.g. https://neko.example/api/auth/callback).
   * The plugin passes this verbatim as the OIDC redirect_uri.
   */
  redirectUri: z.string().min(1),
  /**
   * Opaque CSRF token the core minted before redirecting. The plugin
   * forwards it to the IdP as the OAuth state parameter; the core
   * checks it on callback to defeat login-CSRF. Plugins MUST NOT
   * inspect or mutate it.
   */
  state: z.string().min(1),
  /**
   * Optional email or hostname hint the user typed at the sign-in
   * page. Lets the plugin route to the right downstream IdP without
   * an extra "which company are you?" screen — Scalekit uses this
   * for connection discovery.
   */
  loginHint: z.string().nullable().optional(),
});

export type BeginAuthParams = z.infer<typeof BeginAuthParams>;

export const BeginAuthResult = z.object({
  /**
   * Fully-built provider URL the browser should be redirected to. The
   * plugin owns building the query string (client_id, scope,
   * response_type, state, redirect_uri, connection hints, etc.).
   */
  authorizationUrl: z.string().min(1),
});

export type BeginAuthResult = z.infer<typeof BeginAuthResult>;

export const CompleteAuthParams = z.object({
  /** The `code` query parameter the IdP returned on its redirect. */
  code: z.string().min(1),
  /** The redirect_uri originally passed to begin_auth — required for token exchange. */
  redirectUri: z.string().min(1),
  /**
   * State value the IdP echoed back. The core has already verified it
   * matches what it minted; the plugin gets it for symmetry and so
   * future PKCE-style flows can bind code_verifier to it.
   */
  state: z.string().min(1),
});

export type CompleteAuthParams = z.infer<typeof CompleteAuthParams>;

export const CompleteAuthResult = z.object({
  identity: AuthIdentity,
});

export type CompleteAuthResult = z.infer<typeof CompleteAuthResult>;

export const PluginAuthDeclaration = z.object({
  /**
   * Short human-readable provider label rendered on the sign-in
   * button (e.g. "Scalekit", "Okta", "Keycloak"). The core falls back
   * to the plugin's package name when this is absent.
   */
  providerLabel: z.string().min(1).optional(),
});

export type PluginAuthDeclaration = z.infer<typeof PluginAuthDeclaration>;
