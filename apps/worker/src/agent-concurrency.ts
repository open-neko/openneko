/**
 * Per-backend concurrency cap for the metric agent.
 *
 * Why: globalCap is realized as N independent pg-boss workers (see
 * apps/worker/src/index.ts), so the queue-level pool is shared across
 * tasks. Inside the JS process, claude-agent additionally needs a
 * smaller cap because it runs in-process via the Anthropic SDK and
 * shares the worker's event loop / V8 heap. This semaphore protects
 * the worker from a thundering herd on the SDK path.
 *
 * Hermes is no-op here — each run is its own subprocess, so the only
 * bound it needs is globalCap (already enforced by the pg-boss worker
 * registrations).
 *
 * Cap source: DB scope='agent' on the admin org → default (8). Source of
 * truth is /settings/agent. Changes require a worker restart.
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
  const { claudeAgentCap } = await resolveAgentConcurrency(orgId);
  claudeAgentSem = new Semaphore(claudeAgentCap);
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
