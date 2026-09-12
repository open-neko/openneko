import { afterEach, expect, it, vi } from 'vitest';
import { SandboxPool, type WarmSlot } from '../src/work/sandbox-pool';
afterEach(() => vi.useRealTimers());
it('keeps assignments private, expires idle slots, and destroys changed/failed scopes', async () => {
  vi.useFakeTimers();
  let count = 0;
  const slots: WarmSlot[] = [];
  const pool = new SandboxPool({ size: 1, idleMs: 1000,
    create: async () => { const slot = { name: String(++count), alive: () => true, destroy: vi.fn(async () => {}) }; slots.push(slot); return slot; },
    onError: error => { throw error; } });
  pool.replenish();
  await vi.advanceTimersByTimeAsync(0);
  const session = { key: 'org/alice', scope: 'grants-v1' };
  const first = await pool.acquire(session);
  expect(first.slot?.name).toBe('1');
  await first.release(first.slot!, true);
  const same = await pool.acquire(session);
  expect(same.reused).toBe(true);
  const concurrent = await pool.acquire(session);
  expect(concurrent.slot?.name).not.toBe(same.slot?.name);
  await concurrent.release(concurrent.slot!, true);
  expect(concurrent.slot!.destroy).toHaveBeenCalled();
  await same.release(same.slot!, true);
  await vi.advanceTimersByTimeAsync(0);
  const changed = await pool.acquire({ ...session, scope: 'grants-v2' });
  expect(changed.reused).toBe(false);
  expect(first.slot!.destroy).toHaveBeenCalled();
  const changedSlot = changed.slot ?? { name: "cold", alive: () => true, destroy: vi.fn(async () => {}) };
  await changed.release(changedSlot, true);
  await vi.advanceTimersByTimeAsync(1001);
  expect(changedSlot.destroy).toHaveBeenCalled();
  const bob = await pool.acquire({ key: 'org/bob', scope: 'grants-v2' });
  expect(bob.reused).toBe(false);
  await bob.release(bob.slot!, false);
  expect(bob.slot!.destroy).toHaveBeenCalled();
  await pool.close();
});
