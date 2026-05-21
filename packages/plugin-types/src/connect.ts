import { z } from "zod";

/**
 * Per-operator OAuth/connector contract.
 *
 * Unlike the `auth` capability (singleton SSO, one identity for the
 * whole deployment), `connect` is a non-singleton, per-operator flow.
 * Each operator runs the OAuth dance independently. The plugin
 * exchanges authorization codes for tokens, OpenNeko persists them
 * per-operator in the secrets store, and the plugin refreshes them
 * on demand via the writeback IPC.
 *
 * Used by integrations whose API access is scoped to an individual
 * user account (Google Workspace, GitHub Apps, M365 Graph, etc.).
 */

/** OAuth scope string. The manifest declares the menu; operators opt in to a subset. */
export const ConnectScope = z.string().min(1).max(256);

/**
 * Opaque-from-OpenNeko's-perspective token blob produced by the
 * plugin's complete_connect handler. The plugin owns the shape
 * (access_token, refresh_token, expires_in, id_token vary by
 * provider); OpenNeko persists, re-injects on each action invocation,
 * and triggers refresh when the plugin asks for it. We do not look
 * inside `tokens`.
 */
export const ConnectorCredential = z.object({
  /** Opaque token blob; plugin-owned shape. */
  tokens: z.record(z.string(), z.unknown()),
  /** OAuth scopes granted by the provider on consent. */
  scopes: z.array(ConnectScope).optional(),
  /** Short label cribbed from the manifest's providerLabel — for UI. */
  providerLabel: z.string().min(1).optional(),
  /** ISO timestamp when the operator originally connected. */
  connectedAt: z.string().min(1),
  /** ISO timestamp last touched by a refresh-token writeback. */
  refreshedAt: z.string().min(1).optional(),
});

export type ConnectorCredential = z.infer<typeof ConnectorCredential>;

export const BeginConnectParams = z.object({
  /** Which operator is initiating the connect flow. */
  operatorId: z.string().min(1).max(128),
  /** Where the IdP should send the user after consent. */
  redirectUri: z.string().min(1),
  /**
   * Opaque CSRF token the core minted before redirecting. The plugin
   * forwards it to the IdP as the OAuth `state` parameter; the core
   * checks it on callback. Plugins MUST NOT inspect or mutate it.
   */
  state: z.string().min(1),
  /** OAuth scopes the operator opted into during install. */
  scopes: z.array(ConnectScope).default([]),
  /**
   * Optional PKCE code_verifier the core minted. The plugin computes
   * the matching code_challenge (S256) and includes it in the
   * authorization URL. v1 connectors are expected to use PKCE.
   */
  codeVerifier: z.string().min(1).nullable().optional(),
});

export type BeginConnectParams = z.infer<typeof BeginConnectParams>;

export const BeginConnectResult = z.object({
  /**
   * Fully-built provider URL the browser should be redirected to. The
   * plugin owns building the query string (client_id, scope,
   * response_type, state, redirect_uri, code_challenge, code_challenge_method, etc.).
   */
  authorizationUrl: z.string().min(1),
});

export type BeginConnectResult = z.infer<typeof BeginConnectResult>;

export const CompleteConnectParams = z.object({
  operatorId: z.string().min(1).max(128),
  /** `code` query parameter the IdP returned on redirect. */
  code: z.string().min(1),
  /** Same redirect_uri originally passed to begin_connect — required for token exchange. */
  redirectUri: z.string().min(1),
  /** State value the IdP echoed back. The core has already verified it matches. */
  state: z.string().min(1),
  /** PKCE code_verifier paired with the challenge from begin_connect. */
  codeVerifier: z.string().min(1).nullable().optional(),
  /** Scopes the operator originally requested (passed-through for binding). */
  scopes: z.array(ConnectScope).default([]),
});

export type CompleteConnectParams = z.infer<typeof CompleteConnectParams>;

export const CompleteConnectResult = z.object({
  credential: ConnectorCredential,
});

export type CompleteConnectResult = z.infer<typeof CompleteConnectResult>;

/**
 * Refresh-token rotation. The plugin detects an expired access_token,
 * exchanges the refresh_token with the provider, and returns the new
 * credential. The worker persists it via the secret-writeback IPC so
 * the next action invocation sees the fresh token.
 */
export const RefreshConnectParams = z.object({
  operatorId: z.string().min(1).max(128),
  /** Current credential (read from the secrets store at invocation time). */
  current: ConnectorCredential,
});

export type RefreshConnectParams = z.infer<typeof RefreshConnectParams>;

export const RefreshConnectResult = z.object({
  credential: ConnectorCredential,
});

export type RefreshConnectResult = z.infer<typeof RefreshConnectResult>;
