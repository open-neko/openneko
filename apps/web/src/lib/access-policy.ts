/**
 * One table for who may reach each route. The proxy applies it to every
 * request, and a path with no entry is refused, so a new route cannot ship
 * open by accident (test/lib/access-policy.test.ts fails on a missing entry).
 *
 * Route handlers keep their own checks. This table decides who reaches a
 * route; the handler still decides what that caller may see inside it,
 * including which items they hold.
 *
 * Rules:
 * - `public`: no session, e.g. the sign-in flow.
 * - `token`: the route authenticates its own caller, e.g. a workflow API key.
 * - `signed-in`: a valid session. Solo installs have one operator and pass.
 * - `admin`: a member of Administrators.
 *
 * Prefixes cover an area, so a new route inside one inherits its rule. A new
 * top-level area has no entry, and the test names it until it is listed.
 * A `*` segment matches any one path segment.
 *
 * The Go web front keeps a copy (apps/openneko/internal/webfront/policy.go);
 * its TestPoliciesMatchWeb fails when the two differ.
 *
 * Every API route lives under /api/v1. An older /api/<path> is an alias that
 * next.config.ts rewrites, so it takes the rule of /api/v1/<path>.
 */
export type AccessRule = "public" | "token" | "signed-in" | "admin";

export interface AccessPolicy {
  /** Path prefix, matched against the whole path or a following "/". */
  prefix: string;
  rule: AccessRule;
  why?: string;
}

/** Longest prefix wins, so a narrower entry can sit inside a broader one. */
export const ACCESS_POLICIES: AccessPolicy[] = [
  { prefix: "/signin", rule: "public" },
  { prefix: "/api/v1/auth", rule: "public", why: "the sign-in flow itself" },
  { prefix: "/api/v1/sso", rule: "public", why: "SSO setup runs before the first sign-in" },
  { prefix: "/admin/settings/sso", rule: "public", why: "SSO setup page; the page checks the actor" },
  { prefix: "/api/v1/version", rule: "public" },
  { prefix: "/api/v1/health", rule: "public" },
  { prefix: "/api/v1/workflows/*/runs", rule: "token", why: "workflow API bearer tokens" },
  { prefix: "/api/v1/hooks", rule: "token", why: "Reckon-compatible webhooks carry X-Webhook-Token" },
  { prefix: "/api/v1/channels", rule: "token", why: "channel webhooks carry a provider signature" },
  { prefix: "/api/v1/integrations/connect", rule: "token", why: "OAuth callbacks carry the state cookie" },
  { prefix: "/api/v1/pack-accounts", rule: "token", why: "pack OAuth callbacks carry the state cookie" },

  { prefix: "/admin", rule: "admin" },
  { prefix: "/dev", rule: "admin", why: "development previews; the pages return 404 in production" },
  { prefix: "/api/v1/admin", rule: "admin" },
  { prefix: "/api/v1/settings", rule: "admin" },
  { prefix: "/api/v1/policies", rule: "admin" },
  { prefix: "/api/v1/onboarding", rule: "admin" },
  { prefix: "/onboarding", rule: "admin" },
  { prefix: "/business-profile", rule: "admin" },
  { prefix: "/settings", rule: "admin", why: "older paths that redirect into /admin" },

  { prefix: "/", rule: "signed-in", why: "the home briefing; this entry matches only the home path" },
  { prefix: "/actions", rule: "signed-in" },
  { prefix: "/approvals", rule: "signed-in" },
  { prefix: "/integrations", rule: "signed-in" },
  { prefix: "/library", rule: "signed-in" },
  { prefix: "/memory", rule: "signed-in" },
  { prefix: "/runs", rule: "signed-in" },
  { prefix: "/skills", rule: "signed-in" },
  { prefix: "/chat", rule: "signed-in" },
  { prefix: "/device", rule: "signed-in", why: "approve the installed app's sign-in code" },
  { prefix: "/work", rule: "signed-in" },
  { prefix: "/workflows", rule: "signed-in" },
  { prefix: "/api/v1/action-requests", rule: "signed-in" },
  { prefix: "/api/v1/approvals", rule: "signed-in" },
  { prefix: "/api/v1/insights", rule: "signed-in" },
  { prefix: "/api/v1/integrations", rule: "signed-in" },
  { prefix: "/api/v1/library", rule: "signed-in" },
  { prefix: "/api/v1/my", rule: "signed-in" },
  { prefix: "/api/v1/observations", rule: "signed-in" },
  { prefix: "/api/v1/profile", rule: "signed-in" },
  { prefix: "/api/v1/settings/persona", rule: "signed-in", why: "each person edits their own persona; the route allows only their own row" },
  { prefix: "/profile", rule: "signed-in" },
  { prefix: "/api/v1/subscriptions", rule: "signed-in" },
  { prefix: "/api/v1/bots", rule: "signed-in" },
  { prefix: "/api/v1/work", rule: "signed-in" },
  { prefix: "/api/v1/workflow-runs", rule: "signed-in" },
  { prefix: "/api/v1/workflows", rule: "signed-in" },
];

const byLength = [...ACCESS_POLICIES].sort((a, b) => b.prefix.length - a.prefix.length);

/** Null for a path no entry covers. The proxy refuses those. */
export function accessPolicyFor(pathname: string): AccessPolicy | null {
  const path = apiV1Path(pathname.length > 1 && pathname.endsWith("/") ? pathname.slice(0, -1) : pathname);
  const segments = path.split("/");
  return byLength.find((policy) => {
    if (policy.prefix === "/") return path === "/";
    const prefix = policy.prefix.split("/");
    return prefix.length <= segments.length && prefix.every((part, i) => part === "*" || part === segments[i]);
  }) ?? null;
}

/** The /api/v1 path that an /api/<path> alias reaches. */
export function apiV1Path(pathname: string): string {
  if (pathname === "/api" || !pathname.startsWith("/api/")) return pathname;
  if (pathname === "/api/v1" || pathname.startsWith("/api/v1/")) return pathname;
  return `/api/v1${pathname.slice(4)}`;
}
