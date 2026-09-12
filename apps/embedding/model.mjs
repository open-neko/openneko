import { env, pipeline } from '@huggingface/transformers';

export const MODEL = 'Xenova/all-MiniLM-L6-v2';
export const DIMENSIONS = 384;
// Build-time only download. A running service must use the vendored model.
env.cacheDir = process.env.NEKO_TRANSFORMERS_CACHE || '/app/models';
env.allowRemoteModels = process.argv.includes('--prewarm');
const extractor = await pipeline('feature-extraction', MODEL, {
  dtype: 'q8',
  session_options: { intraOpNumThreads: 1, interOpNumThreads: 1 },
});
export async function embed(text) {
  const result = await extractor(text, { pooling: 'mean', normalize: true });
  return Array.from(result.data);
}
if (process.argv.includes('--prewarm')) {
  const vector = await embed('ready');
  if (vector.length !== DIMENSIONS || !vector.every(Number.isFinite)) {
    throw new Error('Embedding model failed its build-time check');
  }
  console.log(`Vendored ${MODEL}: ${vector.length} dimensions`);
}
