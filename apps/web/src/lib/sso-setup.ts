// SSO setup status for the server-rendered setup page. The setup routes are
// in Go (`openneko web`); both reach the worker's /admin/sso/setup/* endpoints.

export type SsoSetupStatus =
  | "not_started"
  | "awaiting_portal"
  | "polling"
  | "connected"
  | "failed";

export interface SsoSetupConnection {
  status: string;
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

function workerAdminBase(): string {
  const raw = process.env.WORKER_ADMIN_URL ?? "http://127.0.0.1:4100";
  return raw.replace(/\/+$/, "");
}

async function readError(res: Response, label: string): Promise<Error> {
  const text = await res.text().catch(() => "");
  let detail = text;
  try {
    const parsed = JSON.parse(text) as { error?: string };
    if (parsed.error) detail = parsed.error;
  } catch {
    // non-JSON body — use raw text.
  }
  return new Error(`${label} failed (${res.status}): ${detail || res.statusText}`);
}

export async function getSsoSetupStatus(
  orgId: string,
): Promise<SsoSetupStatusResult> {
  const res = await fetch(
    `${workerAdminBase()}/admin/sso/setup/status?orgId=${encodeURIComponent(orgId)}`,
    { method: "GET", cache: "no-store", signal: AbortSignal.timeout(2000) },
  );
  if (!res.ok) throw await readError(res, "SSO setup status");
  return (await res.json()) as SsoSetupStatusResult;
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

