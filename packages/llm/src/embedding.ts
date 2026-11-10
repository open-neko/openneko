export type EmbeddingConfig = {
  url: string;
  apiKey: string | null;
  model: string;
  dimensions: number | null;
};

export class EmbeddingOffError extends Error {
  constructor() {
    super("embedding is off: NEKO_EMBEDDING_URL and NEKO_EMBEDDING_MODEL are not both set");
  }
}

const MAX_DIMENSIONS = 16_000;

export function embeddingConfig(env: NodeJS.ProcessEnv = process.env): EmbeddingConfig | null {
  const url = env.NEKO_EMBEDDING_URL?.trim().replace(/\/+$/, "");
  const model = env.NEKO_EMBEDDING_MODEL?.trim();
  if (!url || !model) return null;
  const dimensions = Number.parseInt(env.NEKO_EMBEDDING_DIMENSIONS?.trim() ?? "", 10);
  return {
    url,
    apiKey: env.NEKO_EMBEDDING_API_KEY?.trim() || null,
    model,
    dimensions: dimensions > 0 && dimensions <= MAX_DIMENSIONS ? dimensions : null,
  };
}

export function embeddingModel(): string | null {
  const config = embeddingConfig();
  if (!config) return null;
  return config.dimensions ? `${config.model}@${config.dimensions}` : config.model;
}

export async function embedText(text: string, timeoutMs = 15_000): Promise<number[]> {
  const config = embeddingConfig();
  if (!config) throw new EmbeddingOffError();
  const trimmed = text.trim();
  if (!trimmed) throw new Error("embedText: text is empty");
  if (trimmed.length > 32000) throw new Error("embedText: text exceeds 32000 characters");
  const response = await fetch(`${config.url}/embeddings`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      ...(config.apiKey ? { authorization: `Bearer ${config.apiKey}` } : {}),
    },
    body: JSON.stringify({
      model: config.model,
      input: trimmed,
      encoding_format: "float",
      ...(config.dimensions ? { dimensions: config.dimensions } : {}),
    }),
    signal: AbortSignal.timeout(timeoutMs),
    redirect: "error",
  });
  if (!response.ok) throw new Error(`embedding service returned HTTP ${response.status}`);
  const body = await response.json();
  const vector = body?.data?.[0]?.embedding;
  if (!Array.isArray(vector) || vector.length === 0 || vector.length > MAX_DIMENSIONS ||
      (config.dimensions !== null && vector.length !== config.dimensions) ||
      !vector.every((value: unknown) => typeof value === "number" && Number.isFinite(value))) {
    throw new Error("embedding service returned an incompatible vector");
  }
  return vector;
}

export function vectorLiteral(vec: number[]): string {
  return `[${vec.join(",")}]`;
}
