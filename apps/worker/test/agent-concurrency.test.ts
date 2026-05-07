/**
 * Per-backend semaphore behaviour. Hermes is no-op; claude-agent caps at the
 * configured cap; capacity 0 means "no cap"; releases re-arm waiters.
 *
 * The cap normally comes from the DB (scope='agent'). For these unit tests
 * we install the semaphore directly via _setClaudeSdkCapacityForTesting so
 * we can exercise the semaphore in isolation without spinning up Postgres.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  _resetAgentConcurrencyForTesting,
  _setClaudeSdkCapacityForTesting,
  acquireAgentSlot,
} from "../src/agent-concurrency.js";

beforeEach(() => {
  _resetAgentConcurrencyForTesting();
});

afterEach(() => {
  _resetAgentConcurrencyForTesting();
});

describe("acquireAgentSlot — hermes", () => {
  it("returns a no-op release function (no blocking)", async () => {
    _setClaudeSdkCapacityForTesting(8); // doesn't matter — hermes is no-op
    const release1 = await acquireAgentSlot("hermes");
    const release2 = await acquireAgentSlot("hermes");
    const release3 = await acquireAgentSlot("hermes");
    expect(typeof release1).toBe("function");
    release1();
    release2();
    release3();
  });
});

describe("acquireAgentSlot — claude-agent", () => {
  it("caps concurrent acquires at semaphore capacity (cap=2)", async () => {
    _setClaudeSdkCapacityForTesting(2);

    const r1 = await acquireAgentSlot("claude-agent");
    const r2 = await acquireAgentSlot("claude-agent");

    let r3Resolved = false;
    const r3p = acquireAgentSlot("claude-agent").then((rel) => {
      r3Resolved = true;
      return rel;
    });

    await new Promise((r) => setTimeout(r, 20));
    expect(r3Resolved).toBe(false);

    r1();
    const r3 = await r3p;
    expect(r3Resolved).toBe(true);

    r2();
    r3();
  });

  it("cap=0 disables blocking", async () => {
    _setClaudeSdkCapacityForTesting(0);

    const releases = await Promise.all(
      Array.from({ length: 50 }, () => acquireAgentSlot("claude-agent")),
    );
    expect(releases).toHaveLength(50);
    for (const r of releases) r();
  });

  it("releases re-arm waiters in FIFO order", async () => {
    _setClaudeSdkCapacityForTesting(1);

    const order: number[] = [];
    const r1 = await acquireAgentSlot("claude-agent");

    const p2 = acquireAgentSlot("claude-agent").then((rel) => {
      order.push(2);
      return rel;
    });
    const p3 = acquireAgentSlot("claude-agent").then((rel) => {
      order.push(3);
      return rel;
    });

    await new Promise((r) => setTimeout(r, 10));
    expect(order).toEqual([]);

    r1();
    const r2 = await p2;
    expect(order).toEqual([2]);
    r2();
    const r3 = await p3;
    expect(order).toEqual([2, 3]);
    r3();
  });
});
