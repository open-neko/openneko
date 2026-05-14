import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent } from "@neko/llm";
import {
  createCoalescingEmit,
  type CoalescingEmitDeps,
} from "@/lib/coalescing-emit";

type PersistedRow = {
  seq: number;
  event: AgentEvent;
};

function makeHarness(flushIdleMs = 100) {
  const persisted: PersistedRow[] = [];
  const notified: Array<{ seq: number; event: AgentEvent }> = [];
  const persistEvent = vi.fn(
    async (args: { seq: number; event: AgentEvent }) => {
      persisted.push({ seq: args.seq, event: args.event });
    },
  ) as unknown as CoalescingEmitDeps["persistEvent"];
  const notify = vi.fn((_runId: string, event: AgentEvent, seq: number) => {
    notified.push({ seq, event });
  });
  const { emit, finalize } = createCoalescingEmit(
    {
      orgId: "org_test",
      threadId: "thr_test",
      runId: "run_test",
      flushIdleMs,
    },
    { persistEvent, notify },
  );
  return { persisted, notified, emit, finalize };
}

describe("createCoalescingEmit", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces consecutive message deltas into one row on flush", async () => {
    const h = makeHarness();

    await h.emit({ type: "message", role: "assistant", content: "Hello " });
    await h.emit({ type: "message", role: "assistant", content: "there, " });
    await h.emit({ type: "message", role: "assistant", content: "Amit." });

    expect(h.persisted).toHaveLength(0);

    await h.finalize();

    expect(h.persisted).toHaveLength(1);
    expect(h.persisted[0].seq).toBe(1);
    expect(h.persisted[0].event).toEqual({
      type: "message",
      role: "assistant",
      content: "Hello there, Amit.",
    });
  });

  it("flushes the message buffer before persisting a non-message event", async () => {
    const h = makeHarness();

    await h.emit({ type: "message", role: "assistant", content: "narrating" });
    await h.emit({ type: "tool_start", id: "t1", name: "Bash" });
    await h.emit({ type: "message", role: "assistant", content: "after tool" });
    await h.finalize();

    expect(h.persisted.map((r) => r.event.type)).toEqual([
      "message",
      "tool_start",
      "message",
    ]);
    expect(h.persisted[0].event).toMatchObject({ content: "narrating" });
    expect(h.persisted[2].event).toMatchObject({ content: "after tool" });
    expect(h.persisted.map((r) => r.seq)).toEqual([1, 2, 3]);
  });

  it("flushes idle buffer when the timer fires", async () => {
    const h = makeHarness(100);

    await h.emit({ type: "message", role: "assistant", content: "first " });
    await h.emit({ type: "message", role: "assistant", content: "burst." });
    expect(h.persisted).toHaveLength(0);

    await vi.advanceTimersByTimeAsync(100);

    expect(h.persisted).toHaveLength(1);
    expect(h.persisted[0].event).toMatchObject({ content: "first burst." });

    // After flush, new deltas start a fresh buffer / row.
    await h.emit({ type: "message", role: "assistant", content: "second " });
    await h.emit({ type: "message", role: "assistant", content: "burst." });
    await h.finalize();

    expect(h.persisted).toHaveLength(2);
    expect(h.persisted[1].event).toMatchObject({ content: "second burst." });
  });

  it("flushes prior buffer when role flips", async () => {
    const h = makeHarness();

    await h.emit({ type: "message", role: "assistant", content: "agent says" });
    await h.emit({ type: "message", role: "user", content: "user says" });
    await h.finalize();

    expect(h.persisted).toHaveLength(2);
    expect(h.persisted[0].event).toEqual({
      type: "message",
      role: "assistant",
      content: "agent says",
    });
    expect(h.persisted[1].event).toEqual({
      type: "message",
      role: "user",
      content: "user says",
    });
  });

  it("persists non-message events without buffering", async () => {
    const h = makeHarness();

    await h.emit({ type: "status", message: "ready" });
    await h.emit({ type: "tool_start", id: "t1", name: "Bash" });
    await h.emit({ type: "tool_end", id: "t1", result: "ok" });

    expect(h.persisted).toHaveLength(3);
    expect(h.persisted.map((r) => r.event.type)).toEqual([
      "status",
      "tool_start",
      "tool_end",
    ]);
  });

  it("finalize is safe to call when buffer is empty", async () => {
    const h = makeHarness();
    await h.finalize();
    expect(h.persisted).toHaveLength(0);
  });

  it("notifies live subscribers with the same seq as DB rows", async () => {
    const h = makeHarness();

    await h.emit({ type: "message", role: "assistant", content: "hi" });
    await h.emit({ type: "status", message: "thinking" });
    await h.finalize();

    expect(h.notified.map((n) => n.seq)).toEqual(h.persisted.map((r) => r.seq));
    expect(h.notified.map((n) => n.event.type)).toEqual([
      "message",
      "status",
    ]);
  });

  it("skips persisting an empty message buffer (no orphan rows)", async () => {
    const h = makeHarness();
    await h.emit({ type: "message", role: "assistant", content: "" });
    await h.finalize();
    expect(h.persisted).toHaveLength(0);
  });
});
