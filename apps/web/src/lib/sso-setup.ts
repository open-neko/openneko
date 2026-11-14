// SSO setup types shared by the admin SSO settings page.
// The /api/v1/sso/setup/* routes and the SSO setup poller run in Go (`openneko web`).

export type SsoSetupStatus =
  | "not_started"
  | "awaiting_portal"
  | "polling"
  | "connected"
  | "failed";

export interface SsoSetupConnection {
  status: "connected" | "in_progress" | "none";
  provider: string | null;
  enabled: boolean;
}

export interface SsoSetupStatusResult {
  status: SsoSetupStatus;
  portalLink: string | null;
  portalLinkExpiresAt: string | null;
  provider: string | null;
  connection: SsoSetupConnection | null;
  lastError: string | null;
  setupCompletedAt: string | null;
  environmentId: string | null;
  organizationId: string | null;
  environmentTier: string | null;
  signInConfigured: boolean;
}

export interface SsoEnvironmentRow {
  id: string;
  name: string | null;
  tier: string | null;
  domain: string | null;
}

export interface SsoOrganizationRow {
  id: string;
  name: string | null;
}
