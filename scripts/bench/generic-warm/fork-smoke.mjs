// Run in the agent image with --network none; no real provider or model calls.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createInterface } from 'node:readline';
import { writeFile, mkdir, readFile } from 'node:fs/promises';
const python = '/usr/local/uv/tools/hermes-agent/bin/python';
const script = '/app/hermes-warm.py';
const home = '/sandbox/.hermes-warm/home';
const daemon = spawn(python, [script, 'serve', '3'], { stdio: ['ignore', 'pipe', 'inherit'] });
const dead = once(daemon, 'exit');
const timeout = setTimeout(() => { daemon.kill('SIGKILL'); process.exit(1); }, 30000);
try {
  await new Promise((resolve, reject) => {
    daemon.stdout.on('data', data => { if (String(data).includes('__openneko_warm_ready__')) resolve(); });
    daemon.once('exit', () => reject(Error('preload exited before readiness')));
  });
  await assert.rejects(readFile(home + '/config.yaml'), { code: 'ENOENT' });
  const ids = new Set();
  for (let turn = 1; turn <= 2; turn++) {
    const checkout = spawn(python, [script, 'checkout']);
    assert.equal((await once(checkout, 'exit'))[0], 0);
    const model = `offline-fork-${turn}`;
    await writeFile(home + '/config.yaml', JSON.stringify({
      model: { default: model, provider: 'offline-bench', context_length: 256000 },
      providers: { 'offline-bench': { base_url: 'http://127.0.0.1:9/v1', api_key: 'not-a-real-key', default_model: model } },
      agent: { max_turns: 1 }, delegation: { orchestrator_enabled: false },
    }));
    await mkdir(home + '/workspace', { recursive: true });
    const child = spawn(python, [script, 'client'], { env: { PATH: process.env.PATH, HOME: home, HERMES_HOME: home, HERMES_DISABLE_LAZY_INSTALLS: '1', HERMES_ACP_SKIP_CONFIGURED_MCP: '1' }, stdio: ['pipe', 'pipe', 'inherit'] });
    const exited = once(child, 'exit');
    const lines = createInterface({ input: child.stdout });
    let id = 0;
    const pending = new Map();
    lines.on('line', line => { try { const frame = JSON.parse(line); const call = pending.get(frame.id); if (call) { pending.delete(frame.id); frame.error ? call.reject(Error(JSON.stringify(frame.error))) : call.resolve(frame.result); } } catch {} });
    const request = (method, params) => new Promise((resolve, reject) => { pending.set(++id, { resolve, reject }); child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n'); });
    try {
      await request('initialize', { protocolVersion: 1, clientCapabilities: {} });
      const session = await request('session/new', { cwd: home + '/workspace', mcpServers: [] });
      assert(JSON.stringify(session.models).includes(model), 'child must read this turn’s model');
      assert(!ids.has(session.sessionId));
      ids.add(session.sessionId);
    } finally {
      child.stdin.end();
      assert.equal((await exited)[0], 0);
      lines.close();
    }
  }
  assert.equal((await dead)[0], 0, 'clean parent must exit on idle timeout');
  console.log('PASS: common preload, two fresh late-configured children, checkout lease, idle exit');
} finally {
  clearTimeout(timeout);
  daemon.kill('SIGTERM');
}
