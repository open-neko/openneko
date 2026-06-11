/**
 * Host networking as seen from inside an OpenShell sandbox. Loopback on the
 * host is unreachable from a box (it resolves to the box itself, and the
 * gateway's egress proxy refuses loopback endpoint rules outright) — the
 * gateway exposes the host as `host.openshell.internal` instead.
 */
export const SANDBOX_HOST_ALIAS = "host.openshell.internal";

const LOOPBACK_HOST =
  /^(localhost|127(?:\.\d{1,3}){3}|0\.0\.0\.0|\[?::1?\]?)$/iu;

export function isLoopbackHost(hostname: string): boolean {
  return LOOPBACK_HOST.test(hostname);
}

/** Rewrite a host-loopback URL to its sandbox-reachable form; other URLs
 *  (and unparseable strings) pass through unchanged. */
export function sandboxReachableUrl(raw: string): string {
  try {
    const u = new URL(raw);
    if (!isLoopbackHost(u.hostname)) return raw;
    u.hostname = SANDBOX_HOST_ALIAS;
    return u.toString();
  } catch {
    return raw;
  }
}
