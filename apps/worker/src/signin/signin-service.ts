// The Scalekit workspace connection in the worker.
// Sign-in (/admin/auth/*) and directory sync run in Go.
import type { ConnectorCredential } from "@neko/secrets";
import type { CoreSecrets } from "../secrets/core-secrets.js";
import {
  beginScalekitConnect,
  completeScalekitConnect,
  SCALEKIT_SETTINGS,
  SCALEKIT_WORKSPACE_LABEL,
  SCALEKIT_WORKSPACE_SCOPES,
  type ScalekitOptions,
} from "./scalekit/provider.js";
import type {
  BeginConnectParams,
  CompleteConnectParams,
  SignInSetting,
} from "./types.js";

export type SignInProviderId = "scalekit" | "magic-link";

interface ProviderSpec {
  label: string;
  settings: SignInSetting[];
}

export const SIGNIN_PROVIDERS: Record<SignInProviderId, ProviderSpec> = {
  scalekit: { label: "Scalekit", settings: SCALEKIT_SETTINGS },
  "magic-link": { label: "Email link", settings: [] },
};

/** Secrets-store section that records which provider is in use. */
export const SIGNIN_SECTION = "signin";

/** Reserved operator slot for the deployment-scoped workspace credential. */
export const DEPLOYMENT_CONNECT_SLOT = "deployment";

export const signInSection = (id: SignInProviderId): string => `signin.${id}`;

export const isSignInProviderId = (value: string): value is SignInProviderId =>
  Object.prototype.hasOwnProperty.call(SIGNIN_PROVIDERS, value);

export class SignInSettingsError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SignInSettingsError";
  }
}

export interface SignInServiceOptions {
  scalekit?: ScalekitOptions;
}

export class SignInService {
  constructor(
    private readonly secrets: CoreSecrets,
    private readonly options: SignInServiceOptions = {},
  ) {}

  selectedProvider(): SignInProviderId | null {
    const id = this.secrets.section(SIGNIN_SECTION).PROVIDER ?? "";
    return isSignInProviderId(id) ? id : null;
  }

  /** Chooses the sign-in provider (null turns sign-in off). */
  async selectProvider(id: SignInProviderId | null): Promise<void> {
    if (id !== null && !isSignInProviderId(id)) throw new SignInSettingsError(`unknown sign-in provider ${id}`);
    if (this.selectedProvider() !== id) await this.secrets.setValues(SIGNIN_SECTION, { PROVIDER: id });
  }

  providerValues(id: SignInProviderId): Record<string, string> {
    return this.secrets.section(signInSection(id));
  }

  /** Writes (string) or deletes (null) declared values of one provider. */
  async setProviderValues(id: SignInProviderId, values: Record<string, string | null>): Promise<void> {
    const declared = new Set(SIGNIN_PROVIDERS[id].settings.map((s) => s.key));
    for (const key of Object.keys(values)) {
      if (!declared.has(key)) throw new SignInSettingsError(`"${key}" is not a ${SIGNIN_PROVIDERS[id].label} setting`);
    }
    await this.secrets.setValues(signInSection(id), values);
  }

  getConnectProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    scopes: string[];
    flow: string;
    credentialScope: string;
  }> {
    return [
      {
        pluginId: "scalekit",
        pluginName: "scalekit",
        providerLabel: SCALEKIT_WORKSPACE_LABEL,
        scopes: SCALEKIT_WORKSPACE_SCOPES,
        flow: "mcp-oauth",
        credentialScope: "deployment",
      },
    ];
  }

  /** No connector is per-operator; the workspace credential is deployment-wide. */
  getOperatorConnectStatus(_operatorId: string): Array<{ pluginName: string; connectedAt: string; scopes?: string[] }> {
    return [];
  }

  getDeploymentConnectStatus(): Array<{ pluginName: string; connectedAt: string; scopes?: string[] }> {
    const credential = this.workspaceCredential();
    return credential ? [{ pluginName: "scalekit", connectedAt: credential.connectedAt, scopes: credential.scopes }] : [];
  }

  private requireConnector(name: string): void {
    if (name !== "scalekit") throw new Error(`unknown connector ${name}`);
  }

  private workspaceCredential(): ConnectorCredential | null {
    return this.secrets.operatorCredential(DEPLOYMENT_CONNECT_SLOT, "scalekit");
  }

  async beginConnect(name: string, params: BeginConnectParams): Promise<{ authorizationUrl: string; oauthState?: string }> {
    this.requireConnector(name);
    return beginScalekitConnect(params, this.providerValues("scalekit"), this.options.scalekit);
  }

  async completeConnect(name: string, params: CompleteConnectParams): Promise<ConnectorCredential> {
    this.requireConnector(name);
    const credential = await completeScalekitConnect(params, this.options.scalekit);
    await this.secrets.setOperatorCredential(DEPLOYMENT_CONNECT_SLOT, "scalekit", credential);
    return credential;
  }

  async disconnect(name: string, _operatorId: string): Promise<boolean> {
    return this.secrets.setOperatorCredential(DEPLOYMENT_CONNECT_SLOT, name, null);
  }
}
