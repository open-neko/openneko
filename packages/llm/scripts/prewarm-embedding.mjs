// Prewarm the transformers.js embedding model into NEKO_TRANSFORMERS_CACHE
// (or the production default at /app/.transformers-cache). Used by the web
// and worker Docker images so the running container never blocks on a
// first-call download.
//
// Run: node packages/llm/scripts/prewarm-embedding.mjs

import { pipeline, env } from "@huggingface/transformers";

const cache =
  process.env.NEKO_TRANSFORMERS_CACHE?.trim() ||
  (process.env.NODE_ENV === "production"
    ? "/app/.transformers-cache"
    : ".cache/transformers");
env.cacheDir = cache;

console.log(`[prewarm-embedding] cache=${cache} model=Xenova/all-MiniLM-L6-v2 dtype=q8`);
const pipe = await pipeline("feature-extraction", "Xenova/all-MiniLM-L6-v2", {
  dtype: "q8",
});
const out = await pipe("ready", { pooling: "mean", normalize: true });
console.log(`[prewarm-embedding] ok — ${(out.data).length}-dim vector produced`);
