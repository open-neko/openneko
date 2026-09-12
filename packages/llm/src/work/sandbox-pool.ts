/** Process-local pool. Assigned slots are never returned to the generic queue. */
export type WarmSlot = {
  name: string;
  alive: () => boolean;
  destroy: () => Promise<void>;
  closed?: Promise<void>;
};
type IdleSlot = {
  slot: WarmSlot;
  scope: string;
  modelScope?: string;
  authorizationScope?: string;
  timer: ReturnType<typeof setTimeout>;
};

export class SandboxPool {
  private generic: WarmSlot[] = [];
  private pending = new Set<Promise<void>>();
  private retry?: ReturnType<typeof setTimeout>;
  private idle = new Map<string, IdleSlot>();
  private busy = new Set<string>();
  private stopped = false;

  constructor(private options: {
    size: number;
    idleMs: number;
    create: () => Promise<WarmSlot>;
    onError: (error: unknown) => void;
    onEvent?: (attributes: Record<string, unknown>) => void;
  }) {}

  private event(attributes: Record<string, unknown>): void {
    try { this.options.onEvent?.(attributes); } catch {}
  }

  replenish(): void {
    if (this.stopped) return;
    this.generic = this.generic.filter(slot => slot.alive());
    while (this.generic.length + this.pending.size < this.options.size) {
      const started = performance.now();
      const preparation = this.options.create().then(async slot => {
        this.event({ outcome: "spare_ready", background: true, slot: slot.name, durationMs: performance.now() - started });
        if (this.stopped) await slot.destroy();
        else {
          this.generic.push(slot);
          void slot.closed?.then(() => {
            const index = this.generic.indexOf(slot);
            if (index < 0) return; // Assigned slots follow their user's idle timeout.
            this.event({ outcome: "evicted", reason: "generic_closed", background: true, slot: slot.name });
            this.generic.splice(index, 1);
            this.replenish();
          });
        }
      }).finally(() => {
        this.pending.delete(preparation);
      });
      this.pending.add(preparation);
      void preparation.catch(error => {
        this.event({ outcome: "spare_failed", background: true, durationMs: performance.now() - started });
        this.options.onError(error);
        if (!this.stopped && !this.retry) {
          this.retry = setTimeout(() => { this.retry = undefined; this.replenish(); }, 1_000);
          this.retry.unref();
        }
      });
    }
  }

  /** Startup readiness and admission share the same in-flight preparation. */
  async ready(signal?: AbortSignal): Promise<void> {
    while (!this.generic.some(slot => slot.alive())) {
      if (this.stopped) throw new Error("Sandbox pool is closed");
      signal?.throwIfAborted();
      this.replenish();
      if (!this.pending.size) throw new Error("Sandbox pool has no capacity");
      const preparation = Promise.race(this.pending);
      if (!signal) { await preparation; continue; }
      await new Promise<void>((resolve, reject) => {
        const abort = () => reject(signal.reason);
        signal.addEventListener("abort", abort, { once: true });
        preparation.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
        if (signal.aborted) abort();
      });
    }
    if (this.stopped) throw new Error("Sandbox pool is closed");
    signal?.throwIfAborted();
  }

  async acquire(user?: { key: string; scope: string; modelScope?: string; authorizationScope?: string }, signal?: AbortSignal): Promise<{
    slot?: WarmSlot;
    reused: boolean;
    release: (slot: WarmSlot | undefined, healthy: boolean) => Promise<void>;
  }> {
    if (this.stopped) throw new Error("Sandbox pool is closed");
    signal?.throwIfAborted();
    // Concurrent turns for the same user get independent disposable slots.
    const retain = user && !this.busy.has(user.key) ? user : undefined;
    if (retain) this.busy.add(retain.key);
    let slot: WarmSlot | undefined;
    let reused = false;
    let reason = !user ? "no_identity" : !retain ? "user_busy" : "no_assigned_slot";
    const old = retain && this.idle.get(retain.key);
    if (old && retain) {
      clearTimeout(old.timer);
      this.idle.delete(retain.key);
      if (old.scope === retain.scope && old.slot.alive()) {
        slot = old.slot;
        reused = true;
      } else {
        reason = old.scope === retain.scope ? "assigned_dead" : old.modelScope !== retain.modelScope ? "model_changed" : old.authorizationScope !== retain.authorizationScope ? "authorization_changed" : "config_changed";
        this.event({ outcome: "evicted", reason, slot: old.slot.name });
        try {
          await old.slot.destroy();
        } catch (error) {
          this.busy.delete(retain.key);
          throw error;
        }
      }
    }
    try {
      while (!slot) {
        if (!this.generic.some(candidate => candidate.alive())) {
          this.event({ outcome: "waiting", reason, pending: this.pending.size });
          await this.ready(signal);
        }
        const candidate = this.generic.shift();
        if (candidate?.alive()) slot = candidate;
      }
    } catch (error) {
      if (retain) this.busy.delete(retain.key);
      throw error;
    }
    this.event({ outcome: reused ? "assigned_hit" : "generic_hit", reason: reused ? "scope_match" : reason, slot: slot?.name });
    this.replenish();
    let released = false;
    return {
      slot, reused,
      release: async (used, healthy) => {
        if (released) return;
        released = true;
        if (retain) this.busy.delete(retain.key);
        // A spare can expire while a long turn runs. Refill before the next request.
        this.replenish();
        if (!used) return;
        if (!this.stopped && retain && healthy && used.alive()) {
          const timer = setTimeout(() => {
            this.event({ outcome: "evicted", reason: "assigned_idle_timeout", slot: used.name, idleMs: this.options.idleMs });
            this.idle.delete(retain.key);
            void used.destroy().catch(this.options.onError);
          }, this.options.idleMs);
          timer.unref();
          this.event({ outcome: "retained", slot: used.name, idleMs: this.options.idleMs });
          this.idle.set(retain.key, { slot: used, scope: retain.scope, modelScope: retain.modelScope, authorizationScope: retain.authorizationScope, timer });
        } else {
          this.event({ outcome: "discarded", reason: this.stopped ? "shutdown" : !retain ? "disposable" : !healthy ? "unhealthy" : "closed" });
          await used.destroy();
        }
      },
    };
  }

  async close(): Promise<void> {
    this.stopped = true;
    clearTimeout(this.retry);
    const slots = [...this.generic, ...[...this.idle.values()].map(value => {
      clearTimeout(value.timer);
      return value.slot;
    })];
    this.generic = [];
    this.idle.clear();
    await Promise.all([...slots.map(slot => slot.destroy()), ...[...this.pending].map(p => p.catch(() => {}))]);
  }
}
