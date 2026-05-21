/**
 * Install-policy read path, shared between apps/web (admin UI) and
 * apps/worker (plugin registry flagging). Persisted in
 * llm_provider_config with scope="install-policy" — same overload
 * pattern as the "agent" scope.
 *
 * The write path lives in apps/web/src/lib/install-policy-settings.ts
 * (admin-only, has UI affordances). The worker only reads.
 */

import { db, and, eq } from "./index";
import { llm_provider_config } from "./schema";

export const INSTALL_POLICY_SCOPE = "install-policy";

export const OFFICIAL_MARKETPLACE_URL =
  "https://open-neko.github.io/plugins/marketplace.json";

export type InstallPolicy = {
  allowUnverified: boolean;
  allowGitUrlInstalls: boolean;
  allowedMarketplaces: string[];
  allowSandboxedSkillEscape: boolean;
};

export const DEFAULT_INSTALL_POLICY: InstallPolicy = {
  allowUnverified: false,
  allowGitUrlInstalls: false,
  allowedMarketplaces: [OFFICIAL_MARKETPLACE_URL],
  allowSandboxedSkillEscape: false,
};

function readBool(value: unknown, fallback: boolean): boolean {
  if (typeof value === "boolean") return value;
  return fallback;
}

function readMarketplaceList(value: unknown): string[] {
  const out = new Set<string>([OFFICIAL_MARKETPLACE_URL]);
  if (!Array.isArray(value)) return [...out];
  for (const entry of value) {
    if (typeof entry !== "string") continue;
    const trimmed = entry.trim();
    if (!trimmed) continue;
    try {
      const url = new URL(trimmed);
      if (url.protocol === "https:") out.add(trimmed);
    } catch {
      /* skip malformed */
    }
  }
  return [...out];
}

export function policyFromConfig(
  config: Record<string, unknown> | null,
): InstallPolicy {
  if (!config) {
    return {
      ...DEFAULT_INSTALL_POLICY,
      allowedMarketplaces: [...DEFAULT_INSTALL_POLICY.allowedMarketplaces],
    };
  }
  return {
    allowUnverified: readBool(config.allowUnverified, DEFAULT_INSTALL_POLICY.allowUnverified),
    allowGitUrlInstalls: readBool(
      config.allowGitUrlInstalls,
      DEFAULT_INSTALL_POLICY.allowGitUrlInstalls,
    ),
    allowedMarketplaces: readMarketplaceList(config.allowedMarketplaces),
    allowSandboxedSkillEscape: readBool(
      config.allowSandboxedSkillEscape,
      DEFAULT_INSTALL_POLICY.allowSandboxedSkillEscape,
    ),
  };
}

/**
 * Read the install policy for an org. Returns DEFAULT_INSTALL_POLICY
 * when no row exists (fresh deployment hasn't customized).
 */
export async function getInstallPolicyForOrg(orgId: string): Promise<InstallPolicy> {
  const rows = await db()
    .select({ config: llm_provider_config.config })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, INSTALL_POLICY_SCOPE),
      ),
    )
    .limit(1);
  return policyFromConfig((rows[0]?.config ?? null) as Record<string, unknown> | null);
}

/**
 * Predicate used by the CLI install path + the worker registry to
 * decide whether a given install source is allowed under the current
 * policy. Centralizes the decision so CLI + worker stay in lockstep.
 */
export function isInstallSourceAllowed(
  policy: InstallPolicy,
  source:
    | { kind: "marketplace"; url: string }
    | { kind: "unverified" }
    | { kind: "git-url" },
): boolean {
  switch (source.kind) {
    case "unverified":
      return policy.allowUnverified;
    case "git-url":
      return policy.allowGitUrlInstalls;
    case "marketplace":
      return policy.allowedMarketplaces.includes(source.url);
  }
}
