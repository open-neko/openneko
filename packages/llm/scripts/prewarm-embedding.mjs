// Prewarm the transformers.js embedding model into NEKO_TRANSFORMERS_CACHE
// (or the production default at /app/.transformers-cache). Used by the web
// and worker Docker images so the running container never blocks on a
// first-call download. A build-time network hiccup (HF 429 / registry
// timeout) must NEVER fail the image build: we retry with backoff, and on
// exhaustion warn and exit 0, leaving the runtime to fetch on first use.
//
// Run: node packages/llm/scripts/prewarm-embedding.mjs

import { pipeline, env } from "@huggingface/transformers";

const cache =
  process.env.NEKO_TRANSFORMERS_CACHE?.trim() ||
  (process.env.NODE_ENV === "production"
    ? "/app/.transformers-cache"
    : ".cache/transformers");
env.cacheDir = cache;

const ATTEMPTS = Number(process.env.PREWARM_ATTEMPTS) || 5;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

console.log(`[prewarm-embedding] cache=${cache} model=Xenova/all-MiniLM-L6-v2 dtype=q8`);
for (let attempt = 1; attempt <= ATTEMPTS; attempt++) {
  try {
    const pipe = await pipeline("feature-extraction", "Xenova/all-MiniLM-L6-v2", {
      dtype: "q8",
    });
    const out = await pipe("ready", { pooling: "mean", normalize: true });
    console.log(`[prewarm-embedding] ok — ${out.data.length}-dim vector produced`);
    process.exit(0);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    if (attempt < ATTEMPTS) {
      const backoff = Math.min(30000, 2000 * 2 ** (attempt - 1));
      console.warn(
        `[prewarm-embedding] attempt ${attempt}/${ATTEMPTS} failed (${msg}); retrying in ${backoff}ms`,
      );
      await sleep(backoff);
    } else {
      console.warn(
        `[prewarm-embedding] giving up after ${ATTEMPTS} attempts (${msg}); ` +
          `leaving cache cold — runtime downloads on first use`,
      );
      process.exit(0);
    }
  }
}
