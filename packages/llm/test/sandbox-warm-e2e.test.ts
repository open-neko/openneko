import { execFile } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { expect, it, vi } from 'vitest';
import { closeSandboxPools, prepareSandboxCapacity } from '../src/work/sandbox-launcher';
import { RECONCILE_COMMAND, syncSandboxDirectory } from '../src/work/sandbox-sync';

// No provider or model call. Uses only a disposable slot on the configured gateway.
// OPENNEKO_OPENSHELL_WARM_E2E=1 OPENNEKO_AGENT_IMAGE=... pnpm --filter @neko/llm exec vitest run test/sandbox-warm-e2e.test.ts
it.skipIf(process.env.OPENNEKO_OPENSHELL_WARM_E2E !== '1' || !process.env.OPENNEKO_AGENT_IMAGE)(
  'prepares a real slot and uploads only changed files through OpenShell', async () => {
    const root = await mkdtemp(path.join(tmpdir(), 'warm-sync-e2e-'));
    let slot = '';
    const originalLog = console.log;
    const logger = vi.spyOn(console, 'log').mockImplementation((...args) => {
      for (const value of args) {
        try { const event = JSON.parse(String(value)); if (event.outcome === 'spare_ready') slot = event.slot; } catch {}
      }
      originalLog(...args);
    });
    const run = (args: string[]) => new Promise<string>((resolve, reject) => {
      const child = execFile('openshell', args, { timeout: 30_000 }, (error, stdout) => error ? reject(error) : resolve(stdout));
      child.stdin?.end();
    });
    try {
      await prepareSandboxCapacity({ agentImage: process.env.OPENNEKO_AGENT_IMAGE!, cpu: '1', memory: '512Mi', warmPoolSize: 1 });
      expect(slot).not.toBe('');
      const source = path.join(root, 'org');
      await mkdir(path.join(source, 'knowledge'), { recursive: true });
      await writeFile(path.join(source, 'knowledge', 'catalog'), 'catalog '.repeat(8192));
      await writeFile(path.join(source, 'job.json'), 'turn1');
      for (let turn = 0; turn < 3; turn++) {
        if (turn === 2) await writeFile(path.join(source, 'job.json'), 'turn2');
        const started = performance.now();
        const stats = await syncSandboxDirectory({ source, destination: '/sandbox/diagnostic', deltaRoot: path.join(root, 'delta'+turn),
          reconcile: manifest => run(['sandbox', 'exec', '-n', slot, '--no-tty', '--', '/usr/local/uv/tools/hermes-agent/bin/python', '-c', RECONCILE_COMMAND, '/sandbox/diagnostic', manifest]),
          upload: async directory => { await run(['sandbox', 'upload', slot, directory, '/sandbox', '--no-git-ignore']); },
        });
        console.log('warm_sync_check', { turn, ...stats, durationMs: performance.now() - started });
        expect(stats.changed).toBe([2, 0, 1][turn]);
        if (turn === 1) expect(stats.bytes).toBe(0);
      }
    } finally {
      await closeSandboxPools();
      logger.mockRestore();
      await rm(root, { recursive: true, force: true });
    }
  }, 180_000,
);
