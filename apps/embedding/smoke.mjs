// Runs inside the built image with --network none. Exercise actual model
// inference and process eviction, not just the lightweight listener's health.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';

const child = spawn('lazy-service', ['--listen', ':5003', '--upstream', 'http://127.0.0.1:5004', '--', 'node', '/app/server.mjs'], {
  env: { ...process.env, OPENNEKO_SERVICE_IDLE_TIMEOUT:'1s' }, stdio:'inherit',
});
const base = 'http://127.0.0.1:5003';
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check) {
  for (let i = 0; i < 150; i++) {
    try { if (await check()) return; } catch {}
    if (child.exitCode !== null) throw new Error('listener exited');
    await sleep(100);
  }
  throw new Error('service state timed out');
}
const health = async () => (await fetch(`${base}/health/ready`)).json();
async function embed(text) {
  const response = await fetch(`${base}/v1/embeddings`, {method:'POST', headers:{'content-type':'application/json'}, body:JSON.stringify({text}), signal:AbortSignal.timeout(150000)});
  assert.equal(response.status, 200);
  const result = await response.json();
  assert.equal(result.model, 'Xenova/all-MiniLM-L6-v2');
  assert.equal(result.vector.length, 384);
  assert.ok(result.vector.every(Number.isFinite));
  assert.ok(Math.abs(result.vector.reduce((sum,v) => sum+v*v,0)-1) < 0.001);
  return result.vector;
}
try {
  await until(async () => (await health()).state === 'sleeping');
  assert.equal((await health()).starts, 0);
  const [a,b] = await Promise.all([embed('OpenNeko document search'), embed('OpenNeko document search')]);
  assert.deepEqual(a,b);
  assert.equal((await health()).starts, 1);
  await until(async () => (await health()).state === 'sleeping');
  assert.deepEqual(await embed('OpenNeko document search'), a);
  assert.equal((await health()).starts, 2);
  console.log('embedding_offline_lazy_restart=ok');
} finally {
  child.kill('SIGTERM');
  if (child.exitCode === null) await once(child,'exit');
}
