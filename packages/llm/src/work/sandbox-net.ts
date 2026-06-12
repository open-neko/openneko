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

/**
 * A name the box can never reach directly: host loopback, or a dot-less
 * docker-compose service name (`graphjin`, `neko-db`) that only resolves on
 * the host's container networks. The box's resolver doesn't know compose
 * names, and even a declared egress endpoint for one fails the proxy's SSRF
 * check (private IP). The compose stack publishes such services on the host
 * at the same port, so the gateway's host alias is the one route that works.
 */
export function isHostLocalName(hostname: string): boolean {
  return isLoopbackHost(hostname) || !hostname.includes(".");
}

/** Rewrite a host-local URL (loopback or compose-internal name) to its
 *  sandbox-reachable form; other URLs (and unparseable strings) pass through
 *  unchanged. */
export function sandboxReachableUrl(raw: string): string {
  try {
    const u = new URL(raw);
    if (!isHostLocalName(u.hostname)) return raw;
    u.hostname = SANDBOX_HOST_ALIAS;
    return u.toString();
  } catch {
    return raw;
  }
}
