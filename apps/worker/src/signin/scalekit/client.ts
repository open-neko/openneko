// Thin Scalekit HTTP helpers used by the Scalekit SCIM directory source.

export const DEFAULT_TIMEOUT_MS = 20_000;

export class ScalekitApiError extends Error {
  constructor(
    message: string,
    public readonly status: number | null,
    public readonly providerError: string | null,
    public readonly providerDescription: string | null = null,
  ) {
    super(message);
    this.name = "ScalekitApiError";
  }
}

export function normalizeEnvironmentUrl(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  if (!trimmed) {
    throw new ScalekitApiError(
      "SCALEKIT_ENVIRONMENT_URL is empty",
      null,
      null,
    );
  }
  if (!/^https:\/\//i.test(trimmed)) {
    throw new ScalekitApiError(
      `SCALEKIT_ENVIRONMENT_URL must be an https URL (got ${trimmed})`,
      null,
      null,
    );
  }
  return trimmed;
}

export async function scalekitRequest<T>(
  fetchImpl: typeof fetch,
  timeoutMs: number,
  url: string,
  init: RequestInit,
  description: string,
): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  let res: Response;
  try {
    res = await fetchImpl(url, { ...init, signal: controller.signal });
  } catch (err) {
    if (err instanceof Error && err.name === "AbortError") {
      throw new ScalekitApiError(
        `Scalekit ${description} timed out after ${timeoutMs}ms`,
        null,
        null,
      );
    }
    throw new ScalekitApiError(
      `Scalekit ${description} network error: ${err instanceof Error ? err.message : String(err)}`,
      null,
      null,
    );
  } finally {
    clearTimeout(timer);
  }

  const text = await res.text().catch(() => "");
  let parsed: unknown = null;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new ScalekitApiError(
        `Scalekit ${description} returned non-JSON (HTTP ${res.status}): ${text.slice(0, 200)}`,
        res.status,
        null,
      );
    }
  }

  if (!res.ok) {
    const errObj =
      parsed && typeof parsed === "object"
        ? (parsed as Record<string, unknown>)
        : {};
    const providerError =
      typeof errObj.error === "string" ? errObj.error : null;
    const providerDescription =
      typeof errObj.error_description === "string"
        ? errObj.error_description
        : null;
    const detail = [
      providerError ?? "no error code",
      providerDescription ? `: ${providerDescription}` : "",
    ].join("");
    throw new ScalekitApiError(
      `Scalekit ${description} returned HTTP ${res.status} (${detail})`,
      res.status,
      providerError,
      providerDescription,
    );
  }

  return (parsed ?? {}) as T;
}
