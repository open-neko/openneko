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
it('shares startup preparation with first admission instead of creating a second sandbox', async () => {
  let finish!: (slot: WarmSlot) => void;
  const slot = { name: 'prepared', alive: () => true, destroy: vi.fn(async () => {}) };
  const create = vi.fn(() => new Promise<WarmSlot>(resolve => { finish = resolve; }));
  const pool = new SandboxPool({ size: 1, idleMs: 1000, create, onError: vi.fn() });
  const ready = pool.ready();
  const admission = pool.acquire({ key: 'alice', scope: 'v1' });
  expect(create).toHaveBeenCalledTimes(1);
  finish(slot);
  await ready;
  const lease = await admission;
  expect(lease.slot).toBe(slot);
  expect(create).toHaveBeenCalledTimes(2); // Only now replenish the consumed spare.
  finish({ ...slot, name: 'replacement' });
  await lease.release(slot, true);
  await pool.close();
});
it('cancels a waiter without leaking its user assignment or cancelling shared preparation', async () => {
  let finish!: (slot: WarmSlot) => void;
  const create = vi.fn(() => new Promise<WarmSlot>(resolve => { finish = resolve; }));
  const pool = new SandboxPool({ size: 1, idleMs: 1000, create, onError: vi.fn() });
  const controller = new AbortController();
  const session = { key: 'alice', scope: 'v1' };
  const cancelled = pool.acquire(session, controller.signal);
  controller.abort(new Error('cancelled'));
  await expect(cancelled).rejects.toThrow('cancelled');
  const waiting = pool.acquire(session);
  const slot = { name: 'ready', alive: () => true, destroy: vi.fn(async () => {}) };
  finish(slot);
  const lease = await waiting;
  finish({ ...slot, name: 'spare' });
  await lease.release(slot, true);
  const reused = await pool.acquire(session);
  expect(reused.reused).toBe(true);
  await reused.release(slot, true);
  await pool.close();
});
it('fails admission on preparation failure and retries without a new request', async () => {
  vi.useFakeTimers();
  const slot = { name: 'recovered', alive: () => true, destroy: vi.fn(async () => {}) };
  const create = vi.fn().mockRejectedValueOnce(new Error('gateway unavailable')).mockResolvedValue(slot);
  const pool = new SandboxPool({ size: 1, idleMs: 1000, create, onError: vi.fn() });
  await expect(pool.acquire({ key: 'alice', scope: 'v1' })).rejects.toThrow('gateway unavailable');
  await vi.advanceTimersByTimeAsync(1000);
  expect(create).toHaveBeenCalledTimes(2);
  const lease = await pool.acquire({ key: 'alice', scope: 'v1' });
  await lease.release(slot, true);
  await pool.close();
});
it('destroys a slot that becomes ready during shutdown and rejects the waiting admission', async () => {
  let finish!: (slot: WarmSlot) => void;
  const slot = { name: 'late', alive: () => true, destroy: vi.fn(async () => {}) };
  const pool = new SandboxPool({ size: 1, idleMs: 1000,
    create: () => new Promise(resolve => { finish = resolve; }), onError: vi.fn() });
  const admission = pool.acquire({ key: 'alice', scope: 'v1' });
  const rejected = expect(admission).rejects.toThrow('closed');
  const closing = pool.close();
  finish(slot);
  await closing; await rejected;
  expect(slot.destroy).toHaveBeenCalledTimes(1);
  await expect(pool.acquire()).rejects.toThrow('closed');
});
it('gives simultaneous waiters distinct slots while sharing pending preparation', async () => {
  const finishes: Array<(slot: WarmSlot) => void> = [];
  const create = vi.fn(() => new Promise<WarmSlot>(resolve => finishes.push(resolve)));
  const pool = new SandboxPool({ size: 1, idleMs: 1000, create, onError: vi.fn() });
  const alice = pool.acquire({ key: 'alice', scope: 'v1' });
  const bob = pool.acquire({ key: 'bob', scope: 'v1' });
  expect(create).toHaveBeenCalledTimes(1);
  const slot = (name: string) => ({ name, alive: () => true, destroy: vi.fn(async () => {}) });
  finishes.shift()!(slot('one'));
  const a = await alice;
  await vi.waitFor(() => expect(finishes).toHaveLength(1));
  finishes.shift()!(slot('two'));
  const b = await bob;
  expect(a.slot).not.toBe(b.slot);
  finishes.shift()!(slot('spare'));
  await a.release(a.slot, true); await b.release(b.slot, true); await pool.close();
});
