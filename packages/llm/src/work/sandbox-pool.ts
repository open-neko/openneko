/** Process-local pool. Assigned slots are never returned to the generic queue. */
export type WarmSlot = {
  name: string;
  alive: () => boolean;
  destroy: () => Promise<void>;
};
type IdleSlot = {
  slot: WarmSlot;
  scope: string;
  timer: ReturnType<typeof setTimeout>;
};

export class SandboxPool {
  private generic: WarmSlot[] = [];
  private pending = 0;
  private idle = new Map<string, IdleSlot>();
  private busy = new Set<string>();
  private stopped = false;

  constructor(private options: {
    size: number;
    idleMs: number;
    create: () => Promise<WarmSlot>;
    onError: (error: unknown) => void;
  }) {}

  replenish(): void {
    if (this.stopped) return;
    this.generic = this.generic.filter(slot => slot.alive());
    while (this.generic.length + this.pending < this.options.size) {
      this.pending++;
      void this.options.create().then(async slot => {
        if (this.stopped) await slot.destroy();
        else this.generic.push(slot);
      }).catch(this.options.onError).finally(() => { this.pending--; });
    }
  }

  async acquire(user?: { key: string; scope: string }): Promise<{
    slot?: WarmSlot;
    reused: boolean;
    release: (slot: WarmSlot | undefined, healthy: boolean) => Promise<void>;
  }> {
    // Concurrent turns for the same user get independent disposable slots.
    const retain = user && !this.busy.has(user.key) ? user : undefined;
    if (retain) this.busy.add(retain.key);
    let slot: WarmSlot | undefined;
    let reused = false;
    const old = retain && this.idle.get(retain.key);
    if (old && retain) {
      clearTimeout(old.timer);
      this.idle.delete(retain.key);
      if (old.scope === retain.scope && old.slot.alive()) {
        slot = old.slot;
        reused = true;
      } else {
        try {
          await old.slot.destroy();
        } catch (error) {
          this.busy.delete(retain.key);
          throw error;
        }
      }
    }
    while (!slot && this.generic.length) {
      const candidate = this.generic.shift()!;
      if (candidate.alive()) slot = candidate;
    }
    this.replenish();
    let released = false;
    return {
      slot, reused,
      release: async (used, healthy) => {
        if (released) return;
        released = true;
        if (retain) this.busy.delete(retain.key);
        if (!used) return;
        if (!this.stopped && retain && healthy && used.alive()) {
          const timer = setTimeout(() => {
            this.idle.delete(retain.key);
            void used.destroy().catch(this.options.onError);
          }, this.options.idleMs);
          timer.unref();
          this.idle.set(retain.key, { slot: used, scope: retain.scope, timer });
        } else {
          await used.destroy();
        }
      },
    };
  }

  async close(): Promise<void> {
    this.stopped = true;
    const slots = [...this.generic, ...[...this.idle.values()].map(value => {
      clearTimeout(value.timer);
      return value.slot;
    })];
    this.generic = [];
    this.idle.clear();
    await Promise.all(slots.map(slot => slot.destroy()));
  }
}
