import { afterEach, expect, it, vi } from 'vitest';
import { SandboxPool, type WarmSlot } from '../src/work/sandbox-pool';
afterEach(() => vi.useRealTimers());
it('replenishes an expired generic spare without waiting for another user request', async () => {
  let expire!: () => void;
  const create = vi.fn(async () => ({
    name: 'spare', alive: () => true, destroy: vi.fn(async () => {}),
    closed: new Promise<void>(resolve => { expire = resolve; }),
  }));
  const pool = new SandboxPool({ size: 1, idleMs: 180_000, create, onError: error => { throw error; } });
  pool.replenish();
  await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  // Let the ready slot enter the pool before simulating its sandbox-side expiry.
  await new Promise(resolve => setImmediate(resolve));
  expire();
  await vi.waitFor(() => expect(create).toHaveBeenCalledTimes(2));
  await pool.close();
  expire();
  await new Promise(resolve => setImmediate(resolve));
  expect(create).toHaveBeenCalledTimes(2);
});
it('keeps a long-running user slot until three minutes after release and replaces expired spares', async () => {
  vi.useFakeTimers();
  let count = 0;
  const create = vi.fn(async () => {
    const expires = Date.now() + 180_000;
    return { name: String(++count), alive: () => Date.now() < expires, destroy: vi.fn(async () => {}) };
  });
  const pool = new SandboxPool({ size: 1, idleMs: 180_000, create, onError: error => { throw error; } });
  pool.replenish();
  await vi.advanceTimersByTimeAsync(0);
  const session = { key: 'org/solo-admin', scope: 'solo-admin-v1' };
  const first = await pool.acquire(session);
  // Active Hermes children do not expire; only the unused generic spare does.
  first.slot!.alive = () => true;
  await vi.advanceTimersByTimeAsync(224_000);
  expect(first.slot!.destroy).not.toHaveBeenCalled();
  await first.release(first.slot, true);
  expect(create).toHaveBeenCalledTimes(3);
  await vi.advanceTimersByTimeAsync(12_000);
  const second = await pool.acquire(session);
  expect(second.reused).toBe(true);
  expect(second.slot).toBe(first.slot);
  await second.release(second.slot, true);
  await vi.advanceTimersByTimeAsync(179_999);
  expect(first.slot!.destroy).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expect(first.slot!.destroy).toHaveBeenCalledTimes(1);
  await pool.close();
});
it('keeps assignments private, expires idle slots, and destroys changed/failed scopes', async () => {
  vi.useFakeTimers();
  let count = 0;
  const slots: WarmSlot[] = [];
  const onEvent = vi.fn();
  const pool = new SandboxPool({ size: 1, idleMs: 1000, onEvent,
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
  expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({ outcome: "assigned_hit" }));
  expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({ reason: "user_busy" }));
  expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({ reason: "config_changed" }));
  expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({ reason: "assigned_idle_timeout" }));
  await pool.close();
});
