/**
 * In-process concurrency cap for the claude-agent backend.
 *
 * pg-boss already bounds the metric_refresh queue (N workers, batchSize=1
 * each) so the queue-level cap is the same. This semaphore is a
 * defense-in-depth bound for the in-process Anthropic SDK path that shares
 * the worker's event loop / V8 heap — without it a thundering herd can
 * exhaust connections before pg-boss ever rebalances.
 *
 * Hermes is a subprocess per run and is no-op here; pg-boss alone bounds it.
 *
 * Cap source: DB scope='agent'.config.globalCap → default. Source of truth
 * is /settings/agent. Changes require a worker restart.
 */

import { getOrgId } from "@neko/db";
import { resolveAgentConcurrency, type AgentBackendId } from "@neko/llm";

class Semaphore {
  private active = 0;
  private waiters: Array<() => void> = [];

  constructor(private readonly capacity: number) {}

  async acquire(): Promise<() => void> {
    if (this.capacity === 0) return () => {};
    if (this.active < this.capacity) {
      this.active++;
      return () => this.release();
    }
    return new Promise((resolve) => {
      this.waiters.push(() => {
        this.active++;
        resolve(() => this.release());
      });
    });
  }

  private release(): void {
    this.active--;
    const next = this.waiters.shift();
    if (next) next();
  }
}

let claudeAgentSem: Semaphore | null = null;

async function getClaudeSdkSemaphore(): Promise<Semaphore> {
  if (claudeAgentSem) return claudeAgentSem;
  const orgId = await getOrgId();
  const { globalCap } = await resolveAgentConcurrency(orgId);
  claudeAgentSem = new Semaphore(globalCap);
  return claudeAgentSem;
}

/**
 * Test-only: drop the cached semaphore so the next acquire rebuilds it from
 * the DB. Not used in production.
 */
export function _resetAgentConcurrencyForTesting(): void {
  claudeAgentSem = null;
}

/**
 * Test-only: install a semaphore with an explicit capacity, bypassing the
 * DB lookup. Lets the unit tests cover semaphore behavior without spinning
 * up Postgres. Not used in production.
 */
export function _setClaudeSdkCapacityForTesting(capacity: number): void {
  claudeAgentSem = new Semaphore(capacity);
}

/**
 * Acquire a concurrency slot for the given backend. Returns a release
 * function — call it (typically in a `finally`) when the run is done.
 * Hermes (and any other backend without a configured cap) is a no-op.
 */
export async function acquireAgentSlot(backend: AgentBackendId): Promise<() => void> {
  if (backend === "claude-agent") {
    const sem = await getClaudeSdkSemaphore();
    return sem.acquire();
  }
  return () => {};
}
