import { execFile } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile, access } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, expect, it, vi } from 'vitest';
import { RECONCILE_COMMAND, syncSandboxDirectory, syncSandboxDirectories } from '../src/work/sandbox-sync';
const roots: string[] = [];
afterEach(async () => { await Promise.all(roots.splice(0).map(root => rm(root, { recursive: true, force: true }))); });
it('skips unchanged files, repairs agent edits, removes stale files and refreshes turn inputs', async () => {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'sandbox-sync-test-'))); roots.push(root);
  const source = path.join(root, 'source'); const destination = path.join(root, 'box', 'org');
  await mkdir(path.join(source, 'knowledge'), { recursive: true });
  await writeFile(path.join(source, 'knowledge', 'catalog'), 'revision 1');
  await writeFile(path.join(source, 'job.json'), 'turn 1');
  const upload = vi.fn(async (delta: string) => { await cp(delta, destination, { recursive: true }); });
  let turn = 0;
  const sync = () => syncSandboxDirectory({ source, destination, deltaRoot: path.join(root, 'delta'+turn++), upload,
    reconcile: manifest => new Promise<string>((resolve, reject) => {
      const child = execFile('python3', ['-c', RECONCILE_COMMAND, destination], (error, stdout) => error ? reject(error) : resolve(stdout));
      child.stdin!.end(manifest);
    }) });
  expect((await sync()).changed).toBe(2);
  expect((await sync()).changed).toBe(0);
  expect(upload).toHaveBeenCalledTimes(1);
  await writeFile(path.join(destination, 'knowledge', 'catalog'), 'agent modification');
  await writeFile(path.join(destination, 'old-turn-secret'), 'old');
  await writeFile(path.join(source, 'job.json'), 'turn 2 fresh credentials');
  expect((await sync()).changed).toBe(2);
  expect(await readFile(path.join(destination, 'knowledge', 'catalog'), 'utf8')).toBe('revision 1');
  expect(await readFile(path.join(destination, 'job.json'), 'utf8')).toBe('turn 2 fresh credentials');
  await expect(access(path.join(destination, 'old-turn-secret'))).rejects.toThrow();
  await rm(path.join(source, 'knowledge'), { recursive: true });
  await sync();
  await expect(access(path.join(destination, 'knowledge'))).rejects.toThrow();
  // A sandbox-created link must not redirect cleanup or uploads outside the tree.
  const outside = path.join(root, 'outside'); await mkdir(outside); await writeFile(path.join(outside, 'keep'), 'safe');
  await symlink(outside, path.join(destination, 'knowledge'));
  await mkdir(path.join(source, 'knowledge')); await writeFile(path.join(source, 'knowledge', 'catalog'), 'revision 2');
  await sync();
  expect(await readFile(path.join(outside, 'keep'), 'utf8')).toBe('safe');
  expect(await readFile(path.join(destination, 'knowledge', 'catalog'), 'utf8')).toBe('revision 2');
});
it('rejects injected paths and source symlinks', async () => {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'sandbox-sync-test-'))); roots.push(root);
  const source = path.join(root, 'source'); await mkdir(source);
  await writeFile(path.join(source, 'config'), 'config');
  const upload = vi.fn();
  const options = { source, destination: '/sandbox/org', deltaRoot: path.join(root, 'delta'), upload,
    reconcile: async () => '__openneko_sync__["../../outside"]' };
  await expect(syncSandboxDirectory(options)).rejects.toThrow('Invalid sandbox file reconciliation');
  expect(upload).not.toHaveBeenCalled();
  await symlink('/tmp', path.join(source, 'link'));
  await expect(syncSandboxDirectory(options)).rejects.toThrow('Unsupported sandbox input');
});

it('reconciles workspace and config in one call and preserves delta validation', async () => {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'sandbox-sync-batch-'))); roots.push(root);
  const directories = await Promise.all(['workspace', 'config'].map(async name => {
    const source = path.join(root, name); const destination = path.join(root, 'box', name);
    await mkdir(source); await writeFile(path.join(source, 'input'), name);
    return { source, destination, deltaRoot: path.join(root, 'delta', name),
      upload: async (delta: string) => { await cp(delta, destination, { recursive: true }); } };
  }));
  const reconcile = vi.fn((manifest: string) => new Promise<string>((resolve, reject) => {
    const child = execFile('python3', ['-c', RECONCILE_COMMAND], (error, stdout) => error ? reject(error) : resolve(stdout));
    child.stdin!.end(manifest);
  }));
  expect((await syncSandboxDirectories({ directories, reconcile })).map(stat => stat.changed)).toEqual([1, 1]);
  expect(reconcile).toHaveBeenCalledTimes(1);
  expect((await syncSandboxDirectories({ directories, reconcile })).map(stat => stat.changed)).toEqual([0, 0]);
  await writeFile(path.join(directories[0]!.destination, 'input'), 'agent edit');
  expect((await syncSandboxDirectories({ directories, reconcile })).map(stat => stat.changed)).toEqual([1, 0]);
  await expect(syncSandboxDirectories({ directories, reconcile: async () => '__openneko_sync__[]' })).rejects.toThrow('batch');
});
