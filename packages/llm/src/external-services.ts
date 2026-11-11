import { embedTexts, embeddingConfig } from "./embedding";
import { serveUrl } from "./files/client";
import { testPrimaryProvider } from "./inference";
import { resolvePrimaryProviderConfig } from "./llm";
import { checkSandbox, sandboxConfig } from "./work/sandbox-service";

export type ExternalServiceId = "inference" | "embedding" | "sandbox" | "file-store";
export type ExternalServiceState = "off" | "ok" | "failing";

export type ExternalServiceStatus = {
  id: ExternalServiceId;
  label: string;
  state: ExternalServiceState;
  where: string | null;
  detail: string;
  checkedAt: string;
};

export type ServiceSettings = { url: string | null; where: string; secrets: string[] };

export type ExternalService = {
  id: ExternalServiceId;
  label: string;
  whenOff: string;
  timeoutMs: number;
  settings(orgId: string): Promise<ServiceSettings | null>;
  check(orgId: string, settings: ServiceSettings, signal: AbortSignal): Promise<{ detail: string; where?: string }>;
};

export function serviceWhere(raw: string): string {
  try {
    const url = new URL(raw);
    if (url.protocol === "unix:") return `unix:${url.pathname}`;
    return `${url.protocol}//${url.host}`;
  } catch {
    return "an address that is not a URL";
  }
}

const inference: ExternalService = {
  id: "inference",
  label: "Inference",
  whenOff: "No model is set up. Agent turns and summaries stop until an admin adds one in Admin, Settings, Models.",
  timeoutMs: 30_000,
  async settings(orgId) {
    const config = await resolvePrimaryProviderConfig(orgId);
    if (config.source === "default" || !config.enabled) return null;
    const url = typeof config.config.url === "string" && config.config.url.trim() ? config.config.url.trim() : null;
    const where = `${config.provider} · ${config.model}${url ? ` · ${serviceWhere(url)}` : ""}`;
    return { url, where, secrets: Object.values(config.secrets) };
  },
  async check(orgId) {
    await testPrimaryProvider(orgId);
    return { detail: "The default model answered a test prompt." };
  },
};

const embedding: ExternalService = {
  id: "embedding",
  label: "Embedding",
  whenOff: "Search uses Postgres full-text search.",
  timeoutMs: 10_000,
  async settings() {
    const config = embeddingConfig();
    if (!config) return null;
    return { url: config.url, where: `${serviceWhere(config.url)} · ${config.model}`, secrets: config.apiKey ? [config.apiKey] : [] };
  },
  async check(_orgId, _settings, signal) {
    const [vector] = await abortable(embedTexts(["OpenNeko status check"], 10_000), signal);
    return { detail: `The service returned a vector of ${vector!.length} numbers.` };
  },
};

const sandbox: ExternalService = {
  id: "sandbox",
  label: "Sandbox",
  whenOff: "The terminal and the script skills are off.",
  timeoutMs: 5_000,
  async settings() {
    const config = sandboxConfig();
    if (!config) return null;
    return { url: config.url, where: serviceWhere(config.url), secrets: config.key ? [config.key] : [] };
  },
  async check(_orgId, _settings, signal) {
    await checkSandbox(sandboxConfig()!, signal);
    return { detail: "The sandbox accepted the key." };
  },
};

const fileStore: ExternalService = {
  id: "file-store",
  label: "File store",
  whenOff: "File search is off: OPENNEKO_SERVE_URL is not set.",
  timeoutMs: 10_000,
  async settings() {
    const url = serveUrl();
    return url ? { url, where: "openneko serve", secrets: [] } : null;
  },
  async check(_orgId, settings, signal) {
    let res: Response;
    try {
      res = await fetch(`${settings.url}/healthz`, { signal });
    } catch (error) {
      if (signal.aborted) throw error;
      throw new Error("openneko serve did not answer");
    }
    if (!res.ok) throw new Error(`openneko serve answered HTTP ${res.status}`);
    const health = (await res.json()) as { file_store?: string; file_store_ok?: boolean; file_store_error?: string; file_store_where?: string };
    const where = health.file_store_where || health.file_store || "openneko serve";
    if (health.file_store_ok === false) throw Object.assign(new Error(health.file_store_error || "the file store failed its check"), { where });
    return {
      detail: health.file_store === "s3" ? "Files are in the bucket." : "Files are in the local folder. No bucket is set.",
      where,
    };
  },
};

export const EXTERNAL_SERVICES: readonly ExternalService[] = [inference, embedding, sandbox, fileStore];

const CACHE_MS = 60_000;
const cache = new Map<string, { at: number; value: Promise<ExternalServiceStatus[]> }>();

export function clearExternalServiceCache(): void {
  cache.clear();
}

/** Results stay for a minute, because the inference and embedding checks each make a paid call. */
export function externalServiceStatus(
  orgId: string,
  options: { refresh?: boolean; services?: readonly ExternalService[] } = {},
): Promise<ExternalServiceStatus[]> {
  const services = options.services ?? EXTERNAL_SERVICES;
  const key = `${orgId}\u0000${services.map((s) => s.id).join(",")}`;
  const hit = cache.get(key);
  if (!options.refresh && hit && Date.now() - hit.at < CACHE_MS) return hit.value;
  const value = Promise.all(services.map((service) => statusOf(orgId, service)));
  cache.set(key, { at: Date.now(), value });
  return value;
}

async function statusOf(orgId: string, service: ExternalService): Promise<ExternalServiceStatus> {
  const base = { id: service.id, label: service.label };
  const checkedAt = () => new Date().toISOString();
  let settings: ServiceSettings | null;
  try {
    settings = await service.settings(orgId);
  } catch (error) {
    return { ...base, state: "failing", where: null, detail: plain(error, []), checkedAt: checkedAt() };
  }
  if (!settings) return { ...base, state: "off", where: null, detail: service.whenOff, checkedAt: checkedAt() };
  const signal = AbortSignal.timeout(service.timeoutMs);
  try {
    const result = await abortable(service.check(orgId, settings, signal), signal);
    return { ...base, state: "ok", where: result.where ?? settings.where, detail: result.detail, checkedAt: checkedAt() };
  } catch (error) {
    const where = (error as { where?: unknown })?.where;
    const detail = signal.aborted ? `No answer within ${service.timeoutMs / 1000} seconds.` : plain(error, settings.secrets);
    return { ...base, state: "failing", where: typeof where === "string" ? where : settings.where, detail, checkedAt: checkedAt() };
  }
}

function abortable<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise<T>((resolve, reject) => {
    const stop = () => reject(signal.reason);
    signal.addEventListener("abort", stop, { once: true });
    work.then(resolve, reject).finally(() => signal.removeEventListener("abort", stop));
  });
}

function plain(error: unknown, secrets: string[]): string {
  let text = (error instanceof Error ? error.message : String(error)).trim() || "The check failed.";
  for (const secret of secrets) {
    if (secret && secret.length >= 4) text = text.split(secret).join("[redacted]");
  }
  return text.length > 300 ? `${text.slice(0, 297)}...` : text;
}
