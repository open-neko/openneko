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
let tabSession: FakeStorage;
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
    tabSession = new FakeStorage();
    vi.stubGlobal("window", {
      localStorage: storage,
      sessionStorage: tabSession,
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
    expect(next.claimNextOfflineQuestion()).toBeNull();
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
    expect(tabSession.data.size).toBe(0);

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
    expect(tab.claimNextOfflineQuestion()?.id).toBe(queued.id);

    const reloaded = await openTab();
    reloaded.setOfflineQueueUser("user-a");
    expect(reloaded.getOfflineQueueSnapshot().map((q) => q.content)).toEqual(["Send me after reload"]);
    const again = await reloaded.withOfflineQueueLock(async () =>
      reloaded.claimNextOfflineQuestion(),
    );
    expect(again?.id).toBe(queued.id);
    expect(JSON.parse(storage.getItem("openneko.work.offlineQueue.user.user-a")!)[0].status).toBe("waiting");
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
        const claimed = tab.claimNextOfflineQuestion();
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
    expect(afterExpiry.claimNextOfflineQuestion()).toBeNull();
    afterExpiry.setOfflineQueueUser("user-b");
    expect(afterExpiry.getOfflineQueueSnapshot()).toEqual([]);
    expect(afterExpiry.claimNextOfflineQuestion()).toBeNull();
    expect(storage.getItem("openneko.work.offlineQueue.user.user-a")).toBeNull();

    tabA.setOfflineQueueUser(null);
    expect(tabA.getOfflineQueueSnapshot()).toEqual([]);
    expect(tabA.claimNextOfflineQuestion()).toBeNull();
  });

  it("sends with one client request id from two tabs when the browser has no Web Locks (FIX18)", async () => {
    vi.stubGlobal("navigator", {});
    const tabA = await openTab();
    tabA.setOfflineQueueUser("user-a");
    const queued = tabA.enqueueOfflineQuestion(question("Same id without locks"));
    const tabB = await openTab();
    tabB.setOfflineQueueUser("user-a");
    const a = tabA.claimNextOfflineQuestion();
    const b = tabB.claimNextOfflineQuestion();
    expect([a?.id, b?.id]).toEqual([queued.id, queued.id]);
    expect(tabA.claimNextOfflineQuestion()).toBeNull();
    tabA.completeOfflineQuestion(queued.id);
    tabB.completeOfflineQuestion(queued.id);
    expect(tabB.getOfflineQueueSnapshot()).toEqual([]);
  });

  it("completes a question on 2xx only; keeps it on 429 and 5xx; marks other refusals failed (FIX12)", async () => {
    const tab = await openTab();
    expect([200, 201, 429, 500, 503, 400, 403, 404, 413].map((status) => tab.offlineSendOutcome(status))).toEqual([
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
    tab.deferOfflineQuestion(tab.claimNextOfflineQuestion()!.id, now + 30_000);
    expect(tab.getOfflineQueueSnapshot()).toMatchObject([{ id: busy.id, status: "waiting", retryAt: now + 30_000 }]);
    expect(tab.claimNextOfflineQuestion()).toBeNull();
    vi.spyOn(Date, "now").mockReturnValue(now + 30_001);
    try {
      expect(tab.claimNextOfflineQuestion()?.id).toBe(busy.id);
    } finally {
      vi.restoreAllMocks();
    }
    tab.failOfflineQuestion(busy.id, "The server refused this question (HTTP 400): message is required");
    expect(tab.getOfflineQueueSnapshot()).toMatchObject([
      { id: busy.id, status: "failed", error: "The server refused this question (HTTP 400): message is required" },
    ]);
    expect(tab.claimNextOfflineQuestion()).toBeNull();
    expect(await tab.cancelOfflineQuestion(busy.id)).toBe(true);
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
    const claimed = tab.claimNextOfflineQuestion()!;
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
        const claimed = tab.claimNextOfflineQuestion()!;
        expect(claimed.id).toBe(queued.id);
        expect(tab.settleOfflineRefusal(claimed.id, res(status), "no")).toBe(outcome);
        expect(tab.getOfflineQueueSnapshot().find((item) => item.id === queued.id)).toMatchObject({
          status: "waiting",
          retryAt: now + tab.offlineRetryDelayMs(res(status)),
        });
        expect(tab.claimNextOfflineQuestion()).toBeNull();
        await tab.cancelOfflineQuestion(queued.id);
      }
      const refused = tab.enqueueOfflineQuestion({ ...question("Not allowed"), threadId: null });
      tab.claimNextOfflineQuestion();
      expect(tab.settleOfflineRefusal(refused.id, res(403), "forbidden")).toBe("refused");
      expect(tab.getOfflineQueueSnapshot()).toMatchObject([
        { id: refused.id, status: "failed", error: "The server refused this question (HTTP 403): forbidden" },
      ]);
      expect(() => tab.settleOfflineRefusal(refused.id, res(201), "")).toThrow();
    } finally {
      vi.restoreAllMocks();
    }
  });
  it("waits on a run in flight and checks the session on another user (FIX18)", async () => {
    const tab = await openTab();
    expect(tab.offlineSendOutcome(409, "run_in_flight")).toBe("retry");
    expect(tab.offlineSendOutcome(409, "user_mismatch")).toBe("signin");
    expect(tab.offlineSendOutcome(409)).toBe("retry");
    tab.setOfflineQueueUser("user-a");
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now);
    try {
      for (const [code, outcome] of [["run_in_flight", "retry"], ["user_mismatch", "signin"]] as const) {
        const queued = tab.enqueueOfflineQuestion({ ...question(`Code ${code}`), threadId: null });
        const claimed = tab.claimNextOfflineQuestion()!;
        const res = { status: 409, headers: new Headers({ "Retry-After": "2" }), code };
        expect(tab.settleOfflineRefusal(claimed.id, res, "busy")).toBe(outcome);
        expect(tab.getOfflineQueueSnapshot().find((item) => item.id === queued.id)).toMatchObject({
          status: "waiting",
          retryAt: now + 5_000,
        });
        await tab.cancelOfflineQuestion(queued.id);
      }
    } finally {
      vi.restoreAllMocks();
    }
  });

  it("keeps questions in memory and says so when storage refuses a write (FIX18b 1)", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    tab.enqueueOfflineQuestion(question("Saved first"));
    expect(tab.isOfflineStorageBroken()).toBe(false);
    const setItem = storage.setItem.bind(storage);
    storage.setItem = () => {
      throw new DOMException("full", "QuotaExceededError");
    };
    tab.enqueueOfflineQuestion(question("Not saved"));
    expect(tab.isOfflineStorageBroken()).toBe(true);
    expect(tab.getOfflineQueueSnapshot().map((q) => q.content)).toEqual(["Saved first", "Not saved"]);
    const first = await tab.withOfflineQueueLock(async () => tab.claimNextOfflineQuestion());
    expect(first?.content).toBe("Saved first");
    tab.completeOfflineQuestion(first!.id);
    expect(tab.getOfflineQueueSnapshot().map((q) => q.content)).toEqual(["Not saved"]);

    storage.setItem = setItem;
    tab.enqueueOfflineQuestion(question("Saved again"));
    expect(tab.isOfflineStorageBroken()).toBe(false);
    expect(JSON.parse(storage.getItem("openneko.work.offlineQueue.user.user-a")!).map((q: { content: string }) => q.content)).toEqual([
      "Not saved",
      "Saved again",
    ]);
  });

  it("sends the questions of a thread in the order typed (FIX18b 2)", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    const q1 = tab.enqueueOfflineQuestion(question("First"));
    const q2 = tab.enqueueOfflineQuestion(question("Second"));
    const other = tab.enqueueOfflineQuestion({ ...question("Other thread"), threadId: "thread-2" });
    expect(tab.claimNextOfflineQuestion()?.id).toBe(q1.id);
    expect(tab.claimNextOfflineQuestion()?.id).toBe(other.id);
    expect(tab.claimNextOfflineQuestion()).toBeNull();
    tab.deferOfflineQuestion(q1.id, Date.now() + 60_000);
    tab.completeOfflineQuestion(other.id);
    expect(tab.claimNextOfflineQuestion()).toBeNull();
    tab.failOfflineQuestion(q1.id, "refused");
    expect(tab.claimNextOfflineQuestion()).toBeNull();
    tab.retryOfflineQuestion(q1.id);
    expect(tab.getOfflineQueueSnapshot()[0]).toMatchObject({ id: q1.id, status: "waiting" });
    expect(tab.getOfflineQueueSnapshot()[0].error).toBeUndefined();
    expect(tab.claimNextOfflineQuestion()?.id).toBe(q1.id);
    tab.completeOfflineQuestion(q1.id);
    expect(tab.claimNextOfflineQuestion()?.id).toBe(q2.id);
  });

  it("gives the thread of the first new-chat question to the rest, and claims from every thread (FIX18b 3)", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    const q1 = tab.enqueueOfflineQuestion({ ...question("New chat one"), threadId: null });
    const q2 = tab.enqueueOfflineQuestion({ ...question("New chat two"), threadId: null });
    const work = tab.enqueueOfflineQuestion({ ...question("Other screen"), threadId: null, threadBase: "/work" });
    const claimed = tab.claimNextOfflineQuestion((item) => item.threadBase === "/chat/t");
    expect(claimed?.id).toBe(q1.id);
    tab.moveNewChatQuestions("/chat/t", "thread-new");
    expect(tab.getOfflineQueueSnapshot().map((q) => [q.id, q.threadId])).toEqual([
      [q1.id, "thread-new"],
      [q2.id, "thread-new"],
      [work.id, null],
    ]);
    tab.completeOfflineQuestion(q1.id);
    expect(tab.claimNextOfflineQuestion()?.id).toBe(q2.id);
    expect(tab.claimNextOfflineQuestion()?.id).toBe(work.id);
  });

  it("deletes other users' questions, drops old ones and does not bring back a cancelled one (FIX18b 4)", async () => {
    const old = new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString();
    storage.setItem(
      "openneko.work.offlineQueue.user.user-a",
      JSON.stringify([{ id: "a1", createdAt: new Date().toISOString(), status: "waiting", ...question("Of user A") }]),
    );
    storage.setItem(
      "openneko.work.offlineQueue.user.user-b",
      JSON.stringify([
        { id: "b-old", createdAt: old, status: "waiting", ...question("Eight days old") },
        { id: "b-far", createdAt: new Date().toISOString(), status: "waiting", retryAt: Date.now() + 10 ** 12, ...question("Far retry") },
      ]),
    );
    const tab = await openTab();
    tab.setOfflineQueueUser("user-b");
    expect(storage.getItem("openneko.work.offlineQueue.user.user-a")).toBeNull();
    const items = tab.getOfflineQueueSnapshot();
    expect(items.map((q) => q.id)).toEqual(["b-far"]);
    expect(items[0].retryAt!).toBeLessThanOrEqual(Date.now() + tab.OFFLINE_RETRY_MAX_MS);

    const claimed = await (async () => {
      vi.spyOn(Date, "now").mockReturnValue(Date.now() + tab.OFFLINE_RETRY_MAX_MS + 1);
      try {
        return tab.claimNextOfflineQuestion();
      } finally {
        vi.restoreAllMocks();
      }
    })();
    expect(claimed?.id).toBe("b-far");
    const other = await openTab();
    other.setOfflineQueueUser("user-b");
    expect(await other.cancelOfflineQuestion("b-far")).toBe(true);
    tab.requeueOfflineQuestion(claimed!);
    expect(tab.getOfflineQueueSnapshot()).toEqual([]);
    expect(storage.getItem("openneko.work.offlineQueue.user.user-b")).toBeNull();
  });

  it("keeps a question typed before the session loads for the first user of this tab only (FIX18b 5)", async () => {
    const tab = await openTab();
    tab.enqueueOfflineQuestion(question("Typed before the session"));
    expect([...tabSession.data.keys()]).toEqual(["openneko.work.offlineQueue.pending"]);
    expect([...storage.data.keys()]).toEqual([]);

    const reloaded = await openTab();
    expect(reloaded.getOfflineQueueSnapshot().map((q) => q.content)).toEqual(["Typed before the session"]);
    expect(reloaded.claimNextOfflineQuestion()).toBeNull();
    reloaded.setOfflineQueueUser("user-a");
    expect(storage.getItem("openneko.work.offlineQueue.user.user-a")).toContain("Typed before the session");
    expect(tabSession.data.size).toBe(0);

    const otherTab = await openTab();
    tabSession = new FakeStorage();
    (window as unknown as { sessionStorage: FakeStorage }).sessionStorage = tabSession;
    otherTab.setOfflineQueueUser("user-b");
    expect(otherTab.getOfflineQueueSnapshot()).toEqual([]);
  });

  it("sends again on 408, 409 and 425, after a delay that grows to a cap (FIX18b 6)", async () => {
    const tab = await openTab();
    expect([408, 409, 425].map((status) => tab.offlineSendOutcome(status))).toEqual(["retry", "retry", "retry"]);
    const res = { status: 503, headers: new Headers() };
    expect([0, 1, 2, 3, 4, 5, 20].map((n) => tab.offlineRetryDelayMs(res, n))).toEqual([
      15_000, 30_000, 60_000, 120_000, 240_000, 300_000, 300_000,
    ]);
    expect([0, 1, 4, 5, 20].map((n) => tab.offlineNetworkDelayMs(n))).toEqual([1000, 2000, 16_000, 30_000, 30_000]);

    tab.setOfflineQueueUser("user-a");
    const q = tab.enqueueOfflineQuestion(question("Grows"));
    const now = Date.now();
    vi.spyOn(Date, "now").mockReturnValue(now);
    try {
      tab.claimNextOfflineQuestion();
      tab.settleOfflineRefusal(q.id, res, "busy");
      expect(tab.getOfflineQueueSnapshot()[0].retryAt).toBe(now + 15_000);
      vi.spyOn(Date, "now").mockReturnValue(now + 15_001);
      tab.claimNextOfflineQuestion();
      tab.settleOfflineRefusal(q.id, res, "busy");
      expect(tab.getOfflineQueueSnapshot()[0].retryAt).toBe(now + 15_001 + 30_000);
      vi.spyOn(Date, "now").mockReturnValue(now + 60_000);
      tab.claimNextOfflineQuestion();
      tab.deferAfterNetworkFailure(q.id);
      expect(tab.getOfflineQueueSnapshot()[0].retryAt).toBe(now + 60_000 + 4000);
    } finally {
      vi.restoreAllMocks();
    }
  });

  it("lets no tab cancel a question while another tab posts it (FIX18b 7)", async () => {
    const tabA = await openTab();
    tabA.setOfflineQueueUser("user-a");
    const q = tabA.enqueueOfflineQuestion(question("In flight"));
    const tabB = await openTab();
    tabB.setOfflineQueueUser("user-a");
    let post = () => {};
    const sending = tabA.withOfflineQueueLock(async () => {
      const claimed = tabA.claimNextOfflineQuestion();
      expect(claimed?.id).toBe(q.id);
      await new Promise<void>((resolve) => (post = resolve));
      tabA.completeOfflineQuestion(q.id);
    });
    await Promise.resolve();
    expect(await tabA.cancelOfflineQuestion(q.id)).toBe(false);
    let cancelled: boolean | null = null;
    const cancel = tabB.cancelOfflineQuestion(q.id).then((v) => (cancelled = v));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(cancelled).toBeNull();
    post();
    await Promise.all([sending, cancel]);
    expect(cancelled).toBe(false);
    expect(storage.getItem("openneko.work.offlineQueue.user.user-a")).toBeNull();
  });
});
