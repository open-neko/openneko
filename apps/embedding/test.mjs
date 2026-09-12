import { test } from 'node:test';
import assert from 'node:assert/strict';
import { embeddingServer } from './server.mjs';

test('validates input, serializes inference, and recovers after a failed call', async () => {
  let active = 0, peak = 0, calls = 0;
  const server = embeddingServer(async text => {
    active++; peak = Math.max(peak, active); calls++;
    try {
      await new Promise(resolve => setTimeout(resolve, 5));
      if (text === 'fail') throw new Error('fixture failure');
      return Array.from({length:384}, (_,i) => i === 0 ? 1 : 0);
    } finally { active--; }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const url = `http://127.0.0.1:${server.address().port}`;
  const post = text => fetch(`${url}/v1/embeddings`, { method:'POST', body:JSON.stringify({text}) });
  try {
    assert.equal((await fetch(`${url}/health/ready`)).status, 200);
    assert.equal(calls, 0);
    assert.equal((await post(' ')).status, 400);
    assert.equal((await post(12)).status, 400);
    assert.equal((await post('x'.repeat(32001))).status, 400);
    const responses = await Promise.all(Array.from({length:10}, () => post('document')));
    for (const response of responses) {
      assert.equal(response.status, 200);
      assert.equal((await response.json()).vector.length, 384);
    }
    assert.equal(peak, 1);
    assert.equal((await post('fail')).status, 503);
    assert.equal((await post('again')).status, 200);
    assert.equal((await (await fetch(`${url}/health/idle`)).json()).idle, true);
  } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
});
