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

  it("puts a failed send back with its id, so the server can match it", async () => {
    const tab = await openTab();
    tab.setOfflineQueueUser("user-a");
    const queued = tab.enqueueOfflineQuestion(question("Retry me"));
    const claimed = tab.claimNextOfflineQuestion("thread-1", "/chat/t")!;
    tab.requeueOfflineQuestion(claimed);
    expect(tab.getOfflineQueueSnapshot()).toEqual([{ ...queued, status: "waiting" }]);
    expect(queued.id).toMatch(/^[A-Za-z0-9._-]{1,128}$/);
  });
});
