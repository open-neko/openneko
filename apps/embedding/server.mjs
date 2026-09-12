import { createServer } from 'node:http';
import { pathToFileURL } from 'node:url';

export const MODEL = 'Xenova/all-MiniLM-L6-v2';
export const DIMENSIONS = 384;

// One model invocation at a time. Bound queued text to avoid competing ONNX
// workspaces and allow the caller's existing retry behavior on overload.
export function embeddingServer(embed) {
  let tail = Promise.resolve();
  let pending = 0;
  const server = createServer(async (req, res) => {
    const reply = (code, body) => {
      res.writeHead(code, { 'content-type': 'application/json' });
      res.end(JSON.stringify(body));
    };
    if (req.method === 'GET' && req.url === '/health/ready') {
      reply(200, { ok: true, model: MODEL, dimensions: DIMENSIONS }); return;
    }
    if (req.method === 'GET' && req.url === '/health/idle') {
      reply(200, { idle: pending === 0 }); return;
    }
    if (req.method !== 'POST' || req.url !== '/v1/embeddings') {
      reply(404, { error: 'not found' }); return;
    }
    if (pending >= 16) { reply(429, { error: 'embedding queue is full' }); return; }
    pending++;
    try {
      let bytes = 0;
      const chunks = [];
      for await (const chunk of req) {
        bytes += chunk.length;
        if (bytes > 128 * 1024) { reply(413, { error: 'input is too large' }); return; }
        chunks.push(chunk);
      }
      let input;
      try { input = JSON.parse(Buffer.concat(chunks).toString('utf8')); }
      catch { reply(400, { error: 'invalid JSON' }); return; }
      if (typeof input?.text !== 'string' || !input.text.trim() || input.text.length > 32000) {
        reply(400, { error: 'text must contain 1–32000 characters' }); return;
      }
      const previous = tail;
      let release;
      tail = new Promise(resolve => { release = resolve; });
      await previous;
      try {
        if (res.destroyed) return;
        const vector = await embed(input.text.trim());
        if (vector.length !== DIMENSIONS || !vector.every(Number.isFinite)) {
          throw new Error('invalid embedding output');
        }
        reply(200, { model: MODEL, dimensions: DIMENSIONS, vector });
      } finally { release(); }
    } catch (error) {
      console.error('[embedding] computation failed:', error.message);
      if (!res.headersSent) reply(503, { error: 'embedding unavailable' });
    } finally { pending--; }
  });
  server.requestTimeout = 120000;
  return server;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const { embed } = await import('./model.mjs');
  embeddingServer(embed).listen(5004, '127.0.0.1');
}
