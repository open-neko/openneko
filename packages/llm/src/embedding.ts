// In-process sentence embeddings via transformers.js. No external API,
// no per-call cost. The model (Xenova/all-MiniLM-L6-v2 quantized, 384-dim,
// ~22MB) loads from a stable repo-local cache first; if it isn't there,
// transformers.js falls back to a one-time network download from HF Hub.
//
// Cache path resolution: NEKO_TRANSFORMERS_CACHE env, otherwise
// packages/llm/.cache/transformers (relative to this file). That way
// `pnpm install` doesn't blow it away, the worker container can mount it
// as a volume to share across rebuilds, and a Dockerfile RUN step can
// pre-warm it for air-gapped deployments.

import { pipeline, env, type FeatureExtractionPipeline } from "@huggingface/transformers";

const EMBEDDING_MODEL = "Xenova/all-MiniLM-L6-v2";
export const EMBEDDING_DIM = 384;

// Cache resolution: NEKO_TRANSFORMERS_CACHE wins. Otherwise the worker
// and web Dockerfiles pre-warm into /app/.transformers-cache so the first
// embedText() call hits disk, not the network. Local-dev fallback: a
// hidden dir under cwd, which the package's .gitignore ignores.
env.cacheDir =
  process.env.NEKO_TRANSFORMERS_CACHE?.trim() ||
  (process.env.NODE_ENV === "production"
    ? "/app/.transformers-cache"
    : ".cache/transformers");
// Local-first, with network fallback if the file isn't on disk yet.
env.allowLocalModels = true;
env.allowRemoteModels = true;

let pipelinePromise: Promise<FeatureExtractionPipeline> | null = null;

function getPipeline(): Promise<FeatureExtractionPipeline> {
  if (!pipelinePromise) {
    pipelinePromise = pipeline("feature-extraction", EMBEDDING_MODEL, {
      // Quantized variant: ~22MB vs ~87MB FP32, with negligible quality
      // loss for short rules-text similarity.
      dtype: "q8",
    }).catch((err) => {
      // Reset so the next caller retries instead of caching a failure.
      pipelinePromise = null;
      throw err;
    });
  }
  return pipelinePromise;
}

// Embed a single piece of text into a 384-dim normalized vector ready
// for cosine-distance comparisons (pgvector `<=>`).
export async function embedText(text: string): Promise<number[]> {
  const trimmed = text.trim();
  if (!trimmed) {
    throw new Error("embedText: text is empty");
  }
  const pipe = await getPipeline();
  const output = await pipe(trimmed, { pooling: "mean", normalize: true });
  return Array.from(output.data as Float32Array);
}

// Format a JS number[] as the literal pgvector accepts when bound as text.
// pgvector wants `[0.1,0.2,...]` (square brackets, comma-separated, no spaces).
export function vectorLiteral(vec: number[]): string {
  return `[${vec.join(",")}]`;
}
