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
    const run = (args: string[], stdin?: string) => new Promise<string>((resolve, reject) => {
      const child = execFile('openshell', args, { timeout: 30_000 }, (error, stdout) => error ? reject(error) : resolve(stdout));
      child.stdin?.end(stdin);
    });
    try {
      await prepareSandboxCapacity({ agentImage: process.env.OPENNEKO_AGENT_IMAGE!, cpu: '1', memory: '512Mi', warmPoolSize: 1 });
      expect(slot).not.toBe('');
      const source = path.join(root, 'org');
      await mkdir(path.join(source, 'knowledge'), { recursive: true });
      await writeFile(path.join(source, 'knowledge', 'catalog'), 'catalog '.repeat(8192));
      await writeFile(path.join(source, 'job.json'), 'turn1');
      // Existing chats can stage hundreds of files: exceed the real 32 KiB
      // argument limit, then verify unchanged reuse and a fresh turn input.
      for (let i = 0; i < 400; i++) await writeFile(path.join(source, 'knowledge', `asset-${i}-${'x'.repeat(80)}`), 'fixture');
      for (let turn = 0; turn < 3; turn++) {
        if (turn === 2) await writeFile(path.join(source, 'job.json'), 'turn2');
        const started = performance.now();
        const stats = await syncSandboxDirectory({ source, destination: '/sandbox/diagnostic', deltaRoot: path.join(root, 'delta'+turn),
          reconcile: manifest => { expect(Buffer.byteLength(manifest)).toBeGreaterThan(32768); return run(['sandbox', 'exec', '-n', slot, '--no-tty', '--', '/usr/local/uv/tools/hermes-agent/bin/python', '-c', RECONCILE_COMMAND, '/sandbox/diagnostic'], manifest); },
          upload: async directory => { await run(['sandbox', 'upload', slot, directory, '/sandbox', '--no-git-ignore']); },
        });
        console.log('warm_sync_check', { turn, ...stats, durationMs: performance.now() - started });
        expect(stats.changed).toBe([402, 0, 1][turn]);
        if (turn === 1) expect(stats.bytes).toBe(0);
      }
    } finally {
      await closeSandboxPools();
      logger.mockRestore();
      await rm(root, { recursive: true, force: true });
    }
  }, 180_000,
);

it.skipIf(process.env.OPENNEKO_OPENSHELL_WARM_E2E !== '1' || !process.env.OPENNEKO_AGENT_IMAGE)(
  'preloads stable org inputs and refreshes an unused spare without replacing it', async () => {
    const root = await mkdtemp(path.join(tmpdir(), 'warm-preload-e2e-'));
    const workspace = { orgRoot: path.join(root, 'org'), knowledgeRoot: path.join(root, 'org/knowledge'), skillsRoot: path.join(root, 'org/skills') };
    const slots: string[] = [];
    const log = console.log;
    const logger = vi.spyOn(console, 'log').mockImplementation((...args) => {
      for (const value of args) { try { const event = JSON.parse(String(value)); if (event.outcome === 'spare_ready') slots.push(event.slot); } catch {} }
      log(...args);
    });
    const run = (args: string[]) => new Promise<string>((resolve, reject) => {
      const child = execFile('openshell', args, { timeout: 15_000 }, (error, stdout, stderr) => error ? reject(new Error(error.message + stderr)) : resolve(stdout));
      child.stdin?.end();
    });
    const read = () => run(['sandbox', 'exec', '-n', slots[0]!, '--no-tty', '--', '/usr/local/uv/tools/hermes-agent/bin/python', '-c', "from pathlib import Path; print(Path('/sandbox/org/knowledge/catalog').read_text()); print(Path('/sandbox/org/knowledge/stale').exists())"]);
    try {
      await mkdir(workspace.knowledgeRoot, { recursive: true }); await mkdir(workspace.skillsRoot);
      await writeFile(path.join(workspace.knowledgeRoot, 'catalog'), 'revision one');
      await writeFile(path.join(workspace.knowledgeRoot, 'stale'), 'old');
      await prepareSandboxCapacity({ agentImage: process.env.OPENNEKO_AGENT_IMAGE!, cpu: '1', memory: '512Mi', warmPoolSize: 1, warmIdleMs: 3000 }, workspace);
      expect(slots).toHaveLength(1);
      expect(await read()).toContain('revision one\nTrue');
      await writeFile(path.join(workspace.knowledgeRoot, 'catalog'), 'revision two');
      await rm(path.join(workspace.knowledgeRoot, 'stale'));
      await vi.waitFor(async () => expect(await read()).toContain('revision two\nFalse'), { timeout: 15_000, interval: 500 });
      await new Promise(resolve => setTimeout(resolve, 3500));
      expect(await read()).toContain('revision two\nFalse');
      expect(slots).toHaveLength(1);
    } finally {
      await closeSandboxPools(); logger.mockRestore(); await rm(root, { recursive: true, force: true });
    }
  }, 90_000,
);
