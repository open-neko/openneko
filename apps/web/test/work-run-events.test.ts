import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { followRunEvents, STREAM_RETRY_MAX_MS } from "../src/app/(work)/work/work-run-events";
import type { ActiveRunStream, WorkEvent } from "../src/app/(work)/work/work-types";

class FakeEventSource {
  static CLOSED = 2;
  static opened: FakeEventSource[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string; lastEventId: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(readonly url: string) {
    FakeEventSource.opened.push(this);
  }
  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
}

function fakeTarget() {
  const handlers = new Map<string, Set<() => void>>();
  return {
    addEventListener: (type: string, fn: () => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(fn);
    },
    removeEventListener: (type: string, fn: () => void) => handlers.get(type)?.delete(fn),
    dispatch: (type: string) => {
      for (const fn of handlers.get(type) ?? []) fn();
    },
  };
}

describe("followRunEvents reconnect", () => {
  let win: ReturnType<typeof fakeTarget>;
  let nav: { onLine: boolean };
  let ref: { current: ActiveRunStream | null };

  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.opened = [];
    win = fakeTarget();
    nav = { onLine: true };
    ref = { current: null };
    vi.stubGlobal("window", {
      ...win,
      setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
      clearTimeout: (id: number) => clearTimeout(id),
    });
    vi.stubGlobal("document", { ...fakeTarget(), visibilityState: "visible" });
    vi.stubGlobal("navigator", nav);
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 503 })));
  });

  afterEach(() => {
    ref.current?.close();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const follow = (events: WorkEvent[] = []) =>
    followRunEvents({
      activeRunStreamRef: ref,
      threadId: "thread-1",
      runId: "run-1",
      afterId: 0,
      onEvent: (_runId, event) => events.push(event),
    });

  it("sets no timer while offline and connects on the online event", async () => {
    void follow();
    expect(FakeEventSource.opened).toHaveLength(1);
    nav.onLine = false;
    FakeEventSource.opened[0]!.onerror?.();
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeEventSource.opened).toHaveLength(1);

    nav.onLine = true;
    win.dispatch("online");
    expect(FakeEventSource.opened).toHaveLength(2);
  });

  it("waits longer after each failed connect while the server is down, up to the cap", async () => {
    void follow();
    const gaps: number[] = [];
    let last = Date.now();
    for (let i = 0; i < 10; i++) {
      FakeEventSource.opened.at(-1)!.onerror?.();
      const before = FakeEventSource.opened.length;
      while (FakeEventSource.opened.length === before) await vi.advanceTimersByTimeAsync(50);
      gaps.push(Date.now() - last);
      last = Date.now();
    }
    for (let i = 1; i < gaps.length; i++) expect(gaps[i]).toBeGreaterThanOrEqual(gaps[i - 1]!);
    expect(gaps.at(-1)).toBe(STREAM_RETRY_MAX_MS);
    expect(STREAM_RETRY_MAX_MS).toBeGreaterThanOrEqual(10_000);
    expect(gaps[0]).toBeLessThan(1000);
  });

  it("starts again from a short delay after the stream opens", async () => {
    const events: WorkEvent[] = [];
    void follow(events);
    for (let i = 0; i < 6; i++) {
      FakeEventSource.opened.at(-1)!.onerror?.();
      const before = FakeEventSource.opened.length;
      while (FakeEventSource.opened.length === before) await vi.advanceTimersByTimeAsync(50);
    }
    const source = FakeEventSource.opened.at(-1)!;
    source.onopen?.();
    source.onmessage?.({ data: JSON.stringify({ type: "status", message: "Working" }), lastEventId: "1" });
    expect(events).toHaveLength(1);
    source.onerror?.();
    const before = FakeEventSource.opened.length;
    await vi.advanceTimersByTimeAsync(300);
    expect(FakeEventSource.opened.length).toBe(before + 1);
  });
});
