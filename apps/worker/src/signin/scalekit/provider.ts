// Scalekit SCIM directory sync of one Scalekit organization, and
// deployment-scoped OAuth consent against the Scalekit workspace MCP server
// that the SSO setup page drives. Every function takes its settings
// explicitly; SignInService reads them from the `signin.scalekit` section of
// the secrets store.
import type { ConnectorCredential } from "@neko/secrets";
import type {
  ApplyDirectoryChangeParams,
  ApplyDirectoryChangeResult,
  DirectoryCapabilities,
  ListDirectoryParams,
  ListDirectoryResult,
} from "../../directory/types.js";
import type {
  BeginConnectParams,
  CompleteConnectParams,
  SignInSetting,
} from "../types.js";
import { createScalekitDirectory } from "./directory.js";
import {
  createMcpOAuth,
  generatePkceVerifier,
  parseOauthState,
  serializeOauthState,
  type OauthState,
} from "./mcp-oauth.js";

export type ScalekitEnv = Readonly<Record<string, string | undefined>>;

const DEFAULT_MCP_URL = "https://mcp.scalekit.com/";

export const SCALEKIT_SETTINGS: SignInSetting[] = [
  { key: "SCALEKIT_ENVIRONMENT_URL", required: true, secret: false },
  { key: "SCALEKIT_CLIENT_ID", required: true, secret: false },
  { key: "SCALEKIT_CLIENT_SECRET", required: true, secret: true },
  { key: "SCALEKIT_ORGANIZATION_ID", required: false, secret: false },
  { key: "SCALEKIT_ENVIRONMENT_ID", required: false, secret: false },
  { key: "SCALEKIT_MCP_URL", required: false, secret: false },
];

export const SCALEKIT_DIRECTORY: DirectoryCapabilities = {
  providerLabel: "Scalekit directory",
  read: { users: true, groups: true, memberships: true },
  write: { createUser: true, deactivateUser: false },
};

export const SCALEKIT_WORKSPACE_LABEL = "Scalekit workspace";

/**
 * Display list of OAuth scopes for the consent screen. The scopes actually
 * requested are discovered from the MCP server's protected resource metadata.
 */
export const SCALEKIT_WORKSPACE_SCOPES = ["wks:read", "wks:write", "env:read", "env:write", "org:read", "org:write"];

/** Test seam: inject fakes instead of constructing the real clients. */
export interface ScalekitOptions {
  createMcpOAuth?: () => ReturnType<typeof createMcpOAuth>;
  createDirectory?: (env: ResolvedEnv & { organizationId: string }) => ReturnType<typeof createScalekitDirectory>;
}

export class ScalekitError extends Error {
  constructor(message: string, public override readonly cause?: unknown) {
    super(message);
    this.name = "ScalekitError";
  }
}

interface ResolvedEnv {
  environmentUrl: string;
  clientId: string;
  clientSecret: string;
}

function resolveEnv(env: ScalekitEnv): ResolvedEnv {
  const environmentUrl = env.SCALEKIT_ENVIRONMENT_URL;
  const clientId = env.SCALEKIT_CLIENT_ID;
  const clientSecret = env.SCALEKIT_CLIENT_SECRET;
  const missing: string[] = [];
  if (!environmentUrl) missing.push("SCALEKIT_ENVIRONMENT_URL");
  if (!clientId) missing.push("SCALEKIT_CLIENT_ID");
  if (!clientSecret) missing.push("SCALEKIT_CLIENT_SECRET");
  if (missing.length > 0) {
    throw new ScalekitError(`${missing.join(", ")} not set — enter them on the Single sign-on settings page`);
  }
  return { environmentUrl: environmentUrl!, clientId: clientId!, clientSecret: clientSecret! };
}

function resolveMcpUrl(env: ScalekitEnv): string {
  return (env.SCALEKIT_MCP_URL || DEFAULT_MCP_URL).trim().replace(/\/+$/, "");
}

const mcpOAuth = (options: ScalekitOptions) => (options.createMcpOAuth ? options.createMcpOAuth() : createMcpOAuth());

function directory(env: ScalekitEnv, options: ScalekitOptions) {
  const resolved = resolveEnv(env);
  const organizationId = env.SCALEKIT_ORGANIZATION_ID?.trim();
  if (!organizationId) {
    throw new ScalekitError("SCALEKIT_ORGANIZATION_ID not set — select the organization on the Single sign-on settings page");
  }
  return (options.createDirectory ?? createScalekitDirectory)({ ...resolved, organizationId });
}

export async function listScalekitDirectory(
  params: ListDirectoryParams,
  env: ScalekitEnv,
  options: ScalekitOptions = {},
): Promise<ListDirectoryResult> {
  return directory(env, options).list(params);
}

export async function applyScalekitDirectoryChange(
  params: ApplyDirectoryChangeParams,
  env: ScalekitEnv,
  options: ScalekitOptions = {},
): Promise<ApplyDirectoryChangeResult> {
  return directory(env, options).apply(params);
}

/**
 * Begin the MCP-OAuth-2.1 dance: discover the protected resource + AS
 * metadata, register the client (DCR), build the PKCE authorization URL,
 * and serialize the in-flight state into oauthState for the web to echo
 * back on complete.
 */
export async function beginScalekitConnect(
  params: BeginConnectParams,
  env: ScalekitEnv,
  options: ScalekitOptions = {},
): Promise<{ authorizationUrl: string; oauthState: string }> {
  if (!params.redirectUri) throw new ScalekitError("params.redirectUri is required");
  if (!params.state) throw new ScalekitError("params.state is required");
  const oauth = mcpOAuth(options);
  const mcpUrl = resolveMcpUrl(env);
  const resource = await oauth.discoverResource(mcpUrl);
  const asUrl = resource.authorizationServers[0];
  if (!asUrl) throw new ScalekitError(`Scalekit MCP server at ${mcpUrl} advertised no authorization server`);
  const as = await oauth.discoverAuthorizationServer(asUrl);
  let clientId = "";
  let clientSecret: string | null = null;
  if (as.registrationEndpoint) {
    const registered = await oauth.registerClient(as.registrationEndpoint, {
      clientName: "openneko-scalekit",
      redirectUris: [params.redirectUri],
    });
    clientId = registered.clientId;
    clientSecret = registered.clientSecret;
  }
  if (!clientId) throw new ScalekitError("Scalekit MCP authorization server did not support client registration (DCR)");
  const codeVerifier = params.codeVerifier ?? generatePkceVerifier();
  const scopes = resource.scopesSupported;
  const authorizationUrl = oauth.buildAuthorizationUrl({
    authorizationEndpoint: as.authorizationEndpoint,
    clientId,
    redirectUri: params.redirectUri,
    state: params.state,
    codeVerifier,
    scopes,
  });
  const oauthState: OauthState = {
    mcpUrl,
    authorizationServerUrl: asUrl,
    tokenEndpoint: as.tokenEndpoint,
    clientId,
    clientSecret,
    codeVerifier,
    scopes,
    redirectUri: params.redirectUri,
  };
  return { authorizationUrl, oauthState: serializeOauthState(oauthState) };
}

/**
 * Exchange the authorization code using the echoed oauthState, returning a
 * credential whose tokens carry everything refresh needs.
 */
export async function completeScalekitConnect(
  params: CompleteConnectParams,
  options: ScalekitOptions = {},
): Promise<ConnectorCredential> {
  if (!params.code) throw new ScalekitError("params.code is required");
  const state = parseOauthState(params.oauthState);
  const tokens = await mcpOAuth(options).exchangeCode({
    tokenEndpoint: state.tokenEndpoint,
    clientId: state.clientId,
    clientSecret: state.clientSecret,
    code: params.code,
    redirectUri: params.redirectUri || state.redirectUri,
    codeVerifier: params.codeVerifier ?? state.codeVerifier,
  });
  return {
    tokens: {
      access_token: tokens.access_token,
      refresh_token: tokens.refresh_token ?? null,
      expires_at: tokens.expires_in != null ? String(Date.now() + tokens.expires_in * 1000) : null,
      token_endpoint: state.tokenEndpoint,
      client_id: state.clientId,
      client_secret: state.clientSecret ?? "",
    },
    scopes: state.scopes.length > 0 ? state.scopes : undefined,
    providerLabel: SCALEKIT_WORKSPACE_LABEL,
    connectedAt: new Date().toISOString(),
  };
}

