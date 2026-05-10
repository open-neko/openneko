/**
 * Typed errors for agent runs that fail because of the upstream LLM
 * provider — either it's load-shed (5xx) or it rejected our
 * credentials (401 / cached-token expired) — not because the agent
 * itself produced bad output.
 *
 * The hermes / claude binaries surface these failures by printing
 * their own error string to stdout (e.g. "API call failed after 3
 * retries: HTTP 503: Gemini HTTP 503 (UNAVAILABLE): ..." or
 * "390114: Authentication token has expired..."). Without detection
 * that string lands in `parseJsonFromOutput` and throws a confusing
 * "no object braces found" — and pg-boss then retries the job 2×
 * more, hammering an already-broken upstream.
 *
 * The worker treats UpstreamProviderError specially: marks
 * processing_job 'failed' with the clean message, then completes (not
 * rejects) the pg-boss job so retries don't burn against a load-shed
 * or auth-broken provider. The user retries via the card's retry
 * button after fixing the upstream condition.
 */

export type UpstreamProviderErrorKind = "5xx" | "auth";

export class UpstreamProviderError extends Error {
  readonly kind: UpstreamProviderErrorKind;
  readonly provider?: string;
  readonly statusCode?: number;
  constructor(
    message: string,
    opts?: { kind?: UpstreamProviderErrorKind; provider?: string; statusCode?: number },
  ) {
    super(message);
    this.name = "UpstreamProviderError";
    this.kind = opts?.kind ?? "5xx";
    this.provider = opts?.provider;
    this.statusCode = opts?.statusCode;
  }
}

// Patterns we've seen in real failures. Order matters — more specific
// patterns first so we capture the named provider when possible.
// `kind` controls the leading message; `provider` is best-effort.
const PATTERNS: ReadonlyArray<{
  regex: RegExp;
  kind?: UpstreamProviderErrorKind;
  provider?: string;
}> = [
  // Auth/credential failures. Hermes prints these (sometimes prefixed
  // with its own numeric error code, e.g. "390114: Authentication
  // token has expired...") when the configured API key is rejected or
  // a cached OAuth/subscription token expired. Retrying won't help —
  // the user must re-provision via /settings/agent or clear the
  // stale Hermes credential cache.
  { regex: /Authentication token has expired/i, kind: "auth" },
  { regex: /must authenticate again/i, kind: "auth" },
  { regex: /\bHTTP\s+401\b/i, kind: "auth" },
  { regex: /\b(?:Unauthorized|invalid[_\s-]*api[_\s-]*key)\b/i, kind: "auth" },

  // Upstream 5xx — provider is load-shed or having an outage.
  { regex: /Gemini\s+HTTP\s+(5\d{2})\b/i, kind: "5xx", provider: "google-gemini" },
  { regex: /Anthropic\s+HTTP\s+(5\d{2})\b/i, kind: "5xx", provider: "anthropic" },
  { regex: /OpenAI\s+HTTP\s+(5\d{2})\b/i, kind: "5xx", provider: "openai" },
  // Generic Hermes phrasing — covers any provider it routes to.
  { regex: /API call failed after \d+ retries:\s*HTTP\s+(5\d{2})\b/i, kind: "5xx" },
  // Bare "HTTP 5xx (UNAVAILABLE|INTERNAL|...)" as a last resort.
  { regex: /\bHTTP\s+(5\d{2})\b.*?(UNAVAILABLE|INTERNAL|SERVICE_UNAVAILABLE|RESOURCE_EXHAUSTED)?/i, kind: "5xx" },
];

function headingFor(kind: UpstreamProviderErrorKind, provider?: string): string {
  const providerLabel = provider ? ` (${provider})` : "";
  if (kind === "auth") {
    return `Provider authentication failed${providerLabel} — re-check the API key in /settings/agent`;
  }
  return `Upstream provider unavailable${providerLabel}`;
}

/**
 * If the agent's stdout looks like an upstream-provider error rather
 * than a malformed JSON answer, return a typed error. Otherwise null.
 *
 * Only inspects the first ~1500 chars to avoid false positives on
 * legitimate JSON that happens to mention HTTP codes inside reasoning.
 */
export function detectUpstreamError(stdout: string): UpstreamProviderError | null {
  const head = stdout.slice(0, 1500).trim();
  // A real JSON answer starts with `{` or ```json fence — bail fast.
  if (head.startsWith("{") || head.startsWith("```")) return null;

  for (const { regex, kind = "5xx", provider } of PATTERNS) {
    const m = head.match(regex);
    if (!m) continue;
    const statusRaw = m[1] ? Number(m[1]) : NaN;
    const statusCode = Number.isFinite(statusRaw) ? statusRaw : undefined;
    const detail = head.slice(0, 300).replace(/\s+/g, " ").trim();
    return new UpstreamProviderError(
      `${headingFor(kind, provider)}: ${detail}`,
      { kind, provider, statusCode },
    );
  }
  return null;
}
