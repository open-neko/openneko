import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type QueueModule = typeof import("../src/app/(work)/work/work-offline-queue");

class FakeStorage {
  data = new Map<string, string>();
  get length() {
    return this.data.size;
  }
  key(i: number) {
    return [...this.data.keys()][i] ?? null;
  }
  getItem(key: string) {
    return this.data.get(key) ?? null;
  }
  setItem(key: string, value: string) {
    this.data.set(key, value);
    for (const fn of storageListeners) fn({ key } as StorageEvent);
  }
  removeItem(key: string) {
    this.data.delete(key);
    for (const fn of storageListeners) fn({ key } as StorageEvent);
  }
}

let storage: FakeStorage;
let storageListeners: Array<(e: StorageEvent) => void>;

function fakeLocks() {
  let tail: Promise<unknown> = Promise.resolve();
  return {
    request(_name: string, fn: () => Promise<unknown>) {
      const run = tail.then(() => fn());
      tail = run.catch(() => undefined);
      return run;
    },
  };
}

async function openTab(): Promise<QueueModule> {
  vi.resetModules();
  return import("../src/app/(work)/work/work-offline-queue");
}

const question = (content: string) => ({
  threadId: "thread-1",
  threadBase: "/chat/t" as const,
  content,
});

describe("offline question queue storage (FIX6b)", () => {
  beforeEach(() => {
    storage = new FakeStorage();
    storageListeners = [];
    vi.stubGlobal("window", {
      localStorage: storage,
      addEventListener: (type: string, fn: (e: StorageEvent) => void) => {
        if (type === "storage") storageListeners.push(fn);
      },
      removeEventListener: () => {},
    });
    vi.stubGlobal("navigator", { locks: fakeLocks() });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("keeps each user's questions under that user's key", async () => {
    const first = await openTab();
    first.setOfflineQueueUser("user-a");
    first.enqueueOfflineQuestion(question("Revenue for user A"));
    expect([...storage.data.keys()]).toContain("openneko.work.offlineQueue.user.user-a");

    const next = await openTab();
    next.setOfflineQueueUser("user-b");
    expect(next.getOfflineQueueSnapshot()).toEqual([]);
    expect(next.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
  });

  it("drops the queue of the old unkeyed version", async () => {
    storage.setItem(
      "openneko.work.offlineQueue",
      JSON.stringify([{ id: "q1", createdAt: "x", status: "waiting", ...question("Old") }]),
    );
    const tab = await openTab();
    tab.setOfflineQueueUser("user-b");
    expect(tab.getOfflineQueueSnapshot()).toEqual([]);
    expect(storage.getItem("openneko.work.offlineQueue")).toBeNull();
  });

  it("forgets every queue and the user at sign-out", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    tab.enqueueOfflineQuestion(question("Private question"));
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}")));
    vi.stubGlobal("setTimeout", (fn: () => void) => {
      fn();
      return 0;
    });
    (window as unknown as { location: { href: string } }).location = { href: "/" };
    const { signOut } = await import("../src/lib/sign-out");
    await signOut();
    expect([...storage.data.keys()].filter((k) => k.startsWith("openneko.work.offlineQueue"))).toEqual([]);
    expect(tab.getOfflineQueueSnapshot()).toEqual([]);
    tab.enqueueOfflineQuestion(question("Typed in the same tab after sign-out"));
    expect([...storage.data.keys()].filter((k) => k.startsWith("openneko.work.offlineQueue"))).toEqual([]);

    const next = await openTab();
    expect(next.getOfflineQueueSnapshot()).toEqual([]);
    next.enqueueOfflineQuestion(question("Typed before the session loads"));
    expect([...storage.data.keys()].filter((k) => k.startsWith("openneko.work.offlineQueue"))).toEqual([]);
    next.setOfflineQueueUser("user-b");
    expect(storage.getItem("openneko.work.offlineQueue.user.user-b")).toContain(
      "Typed before the session loads",
    );
  });

  it("keeps a question that was sending when the page reloaded", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    const queued = tab.enqueueOfflineQuestion(question("Send me after reload"));
    expect(tab.claimNextOfflineQuestion("thread-1", "/chat/t")?.id).toBe(queued.id);

    const reloaded = await openTab();
    reloaded.setOfflineQueueUser("user-a");
    expect(reloaded.getOfflineQueueSnapshot().map((q) => q.content)).toEqual(["Send me after reload"]);
    const again = await reloaded.withOfflineQueueLock(async () =>
      reloaded.claimNextOfflineQuestion("thread-1", "/chat/t"),
    );
    expect(again?.id).toBe(queued.id);
  });

  it("sends a question from one tab only", async () => {
    const tabA = await openTab();
    tabA.setOfflineQueueUser("user-a");
    tabA.enqueueOfflineQuestion(question("Only once"));
    const tabB = await openTab();
    tabB.setOfflineQueueUser("user-a");
    expect(tabB.getOfflineQueueSnapshot()).toHaveLength(1);

    const sent: string[] = [];
    let postA = () => {};
    const flush = (tab: QueueModule, name: string, hold: boolean) =>
      tab.withOfflineQueueLock(async () => {
        const claimed = tab.claimNextOfflineQuestion("thread-1", "/chat/t");
        if (!claimed) return;
        sent.push(`${name}:${claimed.id}`);
        if (hold) await new Promise<void>((resolve) => (postA = resolve));
        tab.completeOfflineQuestion(claimed.id);
      });
    const a = flush(tabA, "A", true);
    const b = flush(tabB, "B", false);
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    postA();
    await Promise.all([a, b]);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatch(/^A:/);
    expect(tabB.getOfflineQueueSnapshot()).toEqual([]);
  });

  it("shows and sends nothing of the last user before the session names the user (FIX12)", async () => {
    const tabA = await openTab();
    tabA.setOfflineQueueUser("user-a");
    tabA.enqueueOfflineQuestion(question("Private question of user A"));

    const afterExpiry = await openTab();
    expect(afterExpiry.getOfflineQueueSnapshot()).toEqual([]);
    expect(afterExpiry.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
    afterExpiry.setOfflineQueueUser("user-b");
    expect(afterExpiry.getOfflineQueueSnapshot()).toEqual([]);
    expect(afterExpiry.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
    expect(storage.getItem("openneko.work.offlineQueue.user.user-a")).toContain("Private question of user A");

    tabA.setOfflineQueueUser(null);
    expect(tabA.getOfflineQueueSnapshot()).toEqual([]);
    expect(tabA.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
  });

  it("sends a question from one tab only when the browser has no Web Locks (FIX12)", async () => {
    vi.stubGlobal("navigator", {});
    const tabA = await openTab();
    tabA.setOfflineQueueUser("user-a");
    const queued = tabA.enqueueOfflineQuestion(question("Only once without locks"));
    const tabB = await openTab();
    tabB.setOfflineQueueUser("user-a");

    const sent: string[] = [];
    let postA = () => {};
    const flush = (tab: QueueModule, name: string, hold: boolean) =>
      tab.withOfflineQueueLock(async () => {
        const claimed = tab.claimNextOfflineQuestion("thread-1", "/chat/t");
        if (!claimed) return;
        sent.push(`${name}:${claimed.id}`);
        if (hold) await new Promise<void>((resolve) => (postA = resolve));
        tab.completeOfflineQuestion(claimed.id);
      });
    const a = flush(tabA, "A", true);
    const b = flush(tabB, "B", false);
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    await b;
    expect(sent).toEqual([`A:${queued.id}`]);
    const stored = JSON.parse(storage.getItem("openneko.work.offlineQueue.user.user-a")!);
    expect(stored[0]).toMatchObject({ status: "sending", claimedBy: expect.any(String), claimedAt: expect.any(Number) });
    postA();
    await a;
    expect(tabB.getOfflineQueueSnapshot()).toEqual([]);
  });

  it("takes over the claim of a closed tab after the lease, with no Web Locks (FIX12)", async () => {
    vi.stubGlobal("navigator", {});
    const closed = await openTab();
    closed.setOfflineQueueUser("user-a");
    const queued = closed.enqueueOfflineQuestion(question("Claimed by a closed tab"));
    expect(closed.claimNextOfflineQuestion("thread-1", "/chat/t")?.id).toBe(queued.id);

    const next = await openTab();
    next.setOfflineQueueUser("user-a");
    expect(next.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now + next.OFFLINE_CLAIM_LEASE_MS + 1);
    try {
      expect(next.claimNextOfflineQuestion("thread-1", "/chat/t")?.id).toBe(queued.id);
    } finally {
      vi.restoreAllMocks();
    }
  });

  it("completes a question on 2xx only; keeps it on 429 and 5xx; marks other refusals failed (FIX12)", async () => {
    const tab = await openTab();
    expect([200, 201, 429, 500, 503, 400, 403, 404, 413].map(tab.offlineSendOutcome)).toEqual([
      "done", "done", "retry", "retry", "retry", "refused", "refused", "refused", "refused",
    ]);
    const res = (status: number, retryAfter?: string) => ({
      status,
      headers: new Headers(retryAfter ? { "Retry-After": retryAfter } : {}),
    });
    expect(tab.offlineRetryDelayMs(res(429, "12"))).toBe(12_000);
    expect(tab.offlineRetryDelayMs(res(429))).toBe(30_000);
    expect(tab.offlineRetryDelayMs(res(503))).toBe(15_000);
    expect(tab.offlineRetryDelayMs(res(503, "100000"))).toBe(300_000);

    tab.setOfflineQueueUser("user-a");
    const busy = tab.enqueueOfflineQuestion(question("Busy server"));
    const now = Date.now();
    tab.deferOfflineQuestion(tab.claimNextOfflineQuestion("thread-1", "/chat/t")!.id, now + 30_000);
    expect(tab.getOfflineQueueSnapshot()).toMatchObject([{ id: busy.id, status: "waiting", retryAt: now + 30_000 }]);
    expect(tab.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
    vi.spyOn(Date, "now").mockReturnValue(now + 30_001);
    try {
      expect(tab.claimNextOfflineQuestion("thread-1", "/chat/t")?.id).toBe(busy.id);
    } finally {
      vi.restoreAllMocks();
    }
    tab.failOfflineQuestion(busy.id, "The server refused this question (HTTP 400): message is required");
    expect(tab.getOfflineQueueSnapshot()).toMatchObject([
      { id: busy.id, status: "failed", error: "The server refused this question (HTTP 400): message is required" },
    ]);
    expect(tab.claimNextOfflineQuestion("thread-1", "/chat/t")).toBeNull();
    expect(tab.cancelOfflineQuestion(busy.id)).toBe(true);
    expect(tab.getOfflineQueueSnapshot()).toEqual([]);
  });

  it("names this browser's push endpoint in the logout request (FIX12)", async () => {
    storage.setItem("openneko.push.endpoint", "https://push.example.com/this-browser");
    const fetchMock = vi.fn<typeof fetch>(async () => new Response("{}"));
    vi.stubGlobal("fetch", fetchMock);
    (window as unknown as { location: { href: string } }).location = { href: "/" };
    const { signOut } = await import("../src/lib/sign-out");
    await signOut();
    const logout = fetchMock.mock.calls.find((call) => String(call[0]) === "/api/v1/auth/logout");
    expect(logout).toBeDefined();
    expect(JSON.parse(String(logout![1]?.body))).toEqual({
      pushEndpoint: "https://push.example.com/this-browser",
    });
  });

  it("puts a failed send back with its id, so the server can match it", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    const queued = tab.enqueueOfflineQuestion(question("Retry me"));
    const claimed = tab.claimNextOfflineQuestion("thread-1", "/chat/t")!;
    tab.requeueOfflineQuestion(claimed);
    expect(tab.getOfflineQueueSnapshot()).toEqual([{ ...queued, status: "waiting" }]);
    expect(queued.id).toMatch(/^[A-Za-z0-9._-]{1,128}$/);
  });

  it("keeps a question on 429, 5xx and 401 with a retry time; fails it on other refusals (FIX15)", async () => {
    const tab = await openTab();
    expect(tab.offlineSendOutcome(401)).toBe("signin");
    tab.setOfflineQueueUser("user-a");
    const res = (status: number) => ({ status, headers: new Headers() });
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now);
    try {
      for (const [status, outcome] of [[503, "retry"], [429, "retry"], [401, "signin"]] as const) {
        const queued = tab.enqueueOfflineQuestion({ ...question(`Status ${status}`), threadId: null });
        const claimed = tab.claimNextOfflineQuestion(null, "/chat/t")!;
        expect(claimed.id).toBe(queued.id);
        expect(tab.settleOfflineRefusal(claimed.id, res(status), "no")).toBe(outcome);
        expect(tab.getOfflineQueueSnapshot().find((item) => item.id === queued.id)).toMatchObject({
          status: "waiting",
          retryAt: now + tab.offlineRetryDelayMs(res(status)),
        });
        expect(tab.claimNextOfflineQuestion(null, "/chat/t")).toBeNull();
        tab.cancelOfflineQuestion(queued.id);
      }
      const refused = tab.enqueueOfflineQuestion({ ...question("Not allowed"), threadId: null });
      tab.claimNextOfflineQuestion(null, "/chat/t");
      expect(tab.settleOfflineRefusal(refused.id, res(403), "forbidden")).toBe("refused");
      expect(tab.getOfflineQueueSnapshot()).toMatchObject([
        { id: refused.id, status: "failed", error: "The server refused this question (HTTP 403): forbidden" },
      ]);
      expect(() => tab.settleOfflineRefusal(refused.id, res(201), "")).toThrow();
    } finally {
      vi.restoreAllMocks();
    }
  });
});

