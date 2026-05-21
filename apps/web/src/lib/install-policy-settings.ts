import "server-only";

import { and, db, eq, llm_provider_config } from "@neko/db";

/**
 * Deployment-wide policy gates for plugin + skill installs.
 *
 * Each switch is a hard floor: when off, the matching install path is
 * refused (CLI errors out; worker flags any pre-existing entries that
 * snuck in before the switch flipped). Defaults are secure-by-default
 * — an operator opts in explicitly to widen the install surface.
 *
 * Storage lives in `llm_provider_config` with scope="install-policy",
 * single row per org (which IS the deployment in OpenNeko's model —
 * setup writes exactly one org row). Same pattern as
 * agent-backend-settings / provider-settings reuses the table.
 */
export type InstallPolicy = {
  /** Allow `openneko install <pkg> --unverified` (bypass every marketplace). */
  allowUnverified: boolean;
  /** Allow `openneko install <git-url-or-local-folder>` for community skills. */
  allowGitUrlInstalls: boolean;
  /** Marketplaces operators have opted into. The official one is always included. */
  allowedMarketplaces: string[];
  /**
   * When installing an untrusted skill, run its shell blocks in a
   * one-shot microVM. Slower but contained — the safety net for
   * "install this skill someone tweeted about" without trusting it
   * with the worker's full process boundary.
   */
  allowSandboxedSkillEscape: boolean;
};

/** Returned to the settings UI alongside the policy itself. */
export type InstallPolicyPayload = {
  policy: InstallPolicy;
  /** "org" when an operator saved a row; "default" when the row is absent. */
  source: "org" | "default";
};

export const OFFICIAL_MARKETPLACE_URL =
  "https://open-neko.github.io/plugins/marketplace.json";

export const INSTALL_POLICY_SCOPE = "install-policy";
const PROVIDER_TAG = "install-policy";

/**
 * Secure-by-default. New deployments don't allow community plugins or
 * unverified npm installs until an operator opts in. The official
 * marketplace is always trusted (without that, operators couldn't
 * install the first-party Slack/Parallel/Scalekit plugins).
 */
export const DEFAULT_POLICY: InstallPolicy = {
  allowUnverified: false,
  allowGitUrlInstalls: false,
  allowedMarketplaces: [OFFICIAL_MARKETPLACE_URL],
  allowSandboxedSkillEscape: false,
};

async function loadRow(orgId: string): Promise<{
  id: string;
  config: Record<string, unknown> | null;
} | null> {
  const rows = await db()
    .select({
      id: llm_provider_config.id,
      config: llm_provider_config.config,
    })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, INSTALL_POLICY_SCOPE),
      ),
    )
    .limit(1);
  return (
    (rows[0] as { id: string; config: Record<string, unknown> | null } | undefined) ?? null
  );
}

function readBool(value: unknown, fallback: boolean): boolean {
  if (typeof value === "boolean") return value;
  return fallback;
}

function readMarketplaceList(value: unknown): string[] {
  if (!Array.isArray(value)) return [...DEFAULT_POLICY.allowedMarketplaces];
  const seen = new Set<string>();
  // Official is always in the set even if the operator removed it from
  // their stored config — they can't un-trust their own first-party
  // marketplace short of forking the binary.
  seen.add(OFFICIAL_MARKETPLACE_URL);
  for (const entry of value) {
    if (typeof entry !== "string") continue;
    const trimmed = entry.trim();
    if (!trimmed) continue;
    if (!isHttpsUrl(trimmed)) continue;
    seen.add(trimmed);
  }
  return [...seen];
}

function isHttpsUrl(raw: string): boolean {
  try {
    const url = new URL(raw);
    return url.protocol === "https:";
  } catch {
    return false;
  }
}

function policyFromConfig(config: Record<string, unknown> | null): InstallPolicy {
  if (!config) return { ...DEFAULT_POLICY, allowedMarketplaces: [...DEFAULT_POLICY.allowedMarketplaces] };
  return {
    allowUnverified: readBool(config.allowUnverified, DEFAULT_POLICY.allowUnverified),
    allowGitUrlInstalls: readBool(
      config.allowGitUrlInstalls,
      DEFAULT_POLICY.allowGitUrlInstalls,
    ),
    allowedMarketplaces: readMarketplaceList(config.allowedMarketplaces),
    allowSandboxedSkillEscape: readBool(
      config.allowSandboxedSkillEscape,
      DEFAULT_POLICY.allowSandboxedSkillEscape,
    ),
  };
}

export async function getInstallPolicy(orgId: string): Promise<InstallPolicy> {
  const row = await loadRow(orgId);
  return policyFromConfig(row?.config ?? null);
}

export async function getInstallPolicyPayload(
  orgId: string,
): Promise<InstallPolicyPayload> {
  const row = await loadRow(orgId);
  return {
    policy: policyFromConfig(row?.config ?? null),
    source: row ? "org" : "default",
  };
}

/** Partial draft from the UI. Anything absent leaves the existing value alone. */
export type InstallPolicyDraft = {
  allowUnverified?: boolean;
  allowGitUrlInstalls?: boolean;
  allowedMarketplaces?: string[];
  allowSandboxedSkillEscape?: boolean;
};

/**
 * Validation errors as a list, so the API can return them all at once
 * (mirrors data-source-settings). Empty list = OK.
 */
export function validatePolicyDraft(draft: InstallPolicyDraft): string[] {
  const errors: string[] = [];
  if (draft.allowedMarketplaces !== undefined) {
    if (!Array.isArray(draft.allowedMarketplaces)) {
      errors.push("allowedMarketplaces must be an array of URLs.");
    } else {
      for (const url of draft.allowedMarketplaces) {
        if (typeof url !== "string") {
          errors.push("allowedMarketplaces entries must be strings.");
          continue;
        }
        if (!isHttpsUrl(url)) {
          errors.push(`Marketplace URL must be https: "${url}".`);
        }
      }
    }
  }
  return errors;
}

export async function saveInstallPolicyDraft(
  orgId: string,
  draft: InstallPolicyDraft,
): Promise<InstallPolicy> {
  const errors = validatePolicyDraft(draft);
  if (errors.length > 0) throw new Error(errors.join(" "));

  const existing = await loadRow(orgId);
  const current = policyFromConfig(existing?.config ?? null);
  const merged: InstallPolicy = {
    allowUnverified:
      draft.allowUnverified !== undefined ? draft.allowUnverified : current.allowUnverified,
    allowGitUrlInstalls:
      draft.allowGitUrlInstalls !== undefined
        ? draft.allowGitUrlInstalls
        : current.allowGitUrlInstalls,
    allowedMarketplaces:
      draft.allowedMarketplaces !== undefined
        ? readMarketplaceList(draft.allowedMarketplaces)
        : current.allowedMarketplaces,
    allowSandboxedSkillEscape:
      draft.allowSandboxedSkillEscape !== undefined
        ? draft.allowSandboxedSkillEscape
        : current.allowSandboxedSkillEscape,
  };

  if (existing) {
    await db()
      .update(llm_provider_config)
      .set({ config: merged, updated_at: new Date() })
      .where(eq(llm_provider_config.id, existing.id));
  } else {
    await db().insert(llm_provider_config).values({
      org_id: orgId,
      scope: INSTALL_POLICY_SCOPE,
      provider: PROVIDER_TAG,
      enabled: true,
      config: merged,
      secrets: {},
    });
  }

  return merged;
}

/**
 * Predicate used by the CLI install path + the worker registry to
 * decide whether to allow a given install source. Centralizes the
 * "where can a plugin come from?" decision so CLI and worker stay in
 * lockstep.
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
