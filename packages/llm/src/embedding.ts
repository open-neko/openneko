import { gatewayCall } from "./gateway-call";

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

export const MAX_EMBEDDING_INPUTS = 64;

export type EmbeddingRequest = { url: string; model: string; dimensions?: number; input: string[] };
export type EmbeddingResult = { vectors: number[][]; model: string; input_tokens?: number };
export type EmbeddingTransport = (request: EmbeddingRequest, key: string, timeoutMs: number) => Promise<EmbeddingResult>;

let transportOverride: EmbeddingTransport | undefined;

export function setEmbeddingTransport(transport: EmbeddingTransport | undefined): void {
  transportOverride = transport;
}

const viaGateway: EmbeddingTransport = (request, key, timeoutMs) =>
  gatewayCall<EmbeddingResult>({
    frame: { type: "embed", embed: request, ...(key ? { model_key: key } : {}) },
    mode: "embed",
    env: key ? { HARNESS_EMBEDDING_API_KEY: key } : {},
    input: request,
    label: "embedding",
    timeoutMs,
  });

/** One vector per text, in order, from the Go gateway in apps/ax-harness/embedding. */
export async function embedTexts(texts: string[], timeoutMs = 15_000): Promise<number[][]> {
  const config = embeddingConfig();
  if (!config) throw new EmbeddingOffError();
  const input = texts.map((text) => text.trim());
  if (input.length === 0 || input.length > MAX_EMBEDDING_INPUTS) throw new Error(`embedTexts: give 1 to ${MAX_EMBEDDING_INPUTS} texts`);
  if (input.some((text) => !text)) throw new Error("embedText: text is empty");
  if (input.some((text) => text.length > 32000)) throw new Error("embedText: text exceeds 32000 characters");
  const request: EmbeddingRequest = {
    url: config.url,
    model: config.model,
    ...(config.dimensions ? { dimensions: config.dimensions } : {}),
    input,
  };
  const result = await (transportOverride ?? viaGateway)(request, config.apiKey ?? "", timeoutMs);
  const vectors = result?.vectors;
  if (!Array.isArray(vectors) || vectors.length !== input.length || !vectors.every((vector) => validVector(vector, config.dimensions))) {
    throw new Error("embedding service returned an incompatible vector");
  }
  return vectors;
}

export async function embedText(text: string, timeoutMs = 15_000): Promise<number[]> {
  const [vector] = await embedTexts([text], timeoutMs);
  return vector!;
}

function validVector(vector: unknown, dimensions: number | null): vector is number[] {
  return Array.isArray(vector) && vector.length > 0 && vector.length <= MAX_DIMENSIONS &&
    (dimensions === null || vector.length === dimensions) &&
    vector.every((value: unknown) => typeof value === "number" && Number.isFinite(value));
}

export function vectorLiteral(vec: number[]): string {
  return `[${vec.join(",")}]`;
}
