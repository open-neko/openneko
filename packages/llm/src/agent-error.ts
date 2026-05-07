/**
 * Typed errors for agent runs that fail because of an upstream LLM
 * provider, not because the agent itself produced bad output.
 *
 * The hermes / claude binaries surface provider 5xx errors by printing
 * their own error string to stdout (e.g. "API call failed after 3
 * retries: HTTP 503: Gemini HTTP 503 (UNAVAILABLE): ..."). Without this
 * detection that string lands in `parseJsonFromOutput` and throws a
 * confusing "no object braces found" — and pg-boss then retries the job
 * 2× more, hammering the same overloaded provider.
 *
 * The worker treats UpstreamProviderError specially: marks
 * processing_job 'failed' with the clean message, then completes (not
 * rejects) the pg-boss job so retries don't burn against a load-shed
 * provider. The user retries via the card's retry button when ready.
 */

export class UpstreamProviderError extends Error {
  readonly provider?: string;
  readonly statusCode?: number;
  constructor(
    message: string,
    opts?: { provider?: string; statusCode?: number },
  ) {
    super(message);
    this.name = "UpstreamProviderError";
    this.provider = opts?.provider;
    this.statusCode = opts?.statusCode;
  }
}

// Patterns we've seen in real failures. Order matters — more specific
// patterns first so we capture the named provider when possible.
const PATTERNS: ReadonlyArray<{ regex: RegExp; provider?: string }> = [
  { regex: /Gemini\s+HTTP\s+(\d{3})\b/i, provider: "google-gemini" },
  { regex: /Anthropic\s+HTTP\s+(\d{3})\b/i, provider: "anthropic" },
  { regex: /OpenAI\s+HTTP\s+(\d{3})\b/i, provider: "openai" },
  // Generic Hermes phrasing — covers any provider it routes to.
  { regex: /API call failed after \d+ retries:\s*HTTP\s+(\d{3})\b/i },
  // Bare "HTTP 5xx (UNAVAILABLE|INTERNAL|...)" as a last resort.
  { regex: /\bHTTP\s+(5\d{2})\b.*?(UNAVAILABLE|INTERNAL|SERVICE_UNAVAILABLE|RESOURCE_EXHAUSTED)?/i },
];

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

  for (const { regex, provider } of PATTERNS) {
    const m = head.match(regex);
    if (!m) continue;
    const statusRaw = Number(m[1]);
    const statusCode = Number.isFinite(statusRaw) ? statusRaw : undefined;
    const providerLabel = provider ? ` (${provider})` : "";
    const detail = head.slice(0, 300).replace(/\s+/g, " ").trim();
    return new UpstreamProviderError(
      `Upstream provider unavailable${providerLabel}: ${detail}`,
      { provider, statusCode },
    );
  }
  return null;
}
