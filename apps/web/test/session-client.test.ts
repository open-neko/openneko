import { afterEach, describe, expect, it, vi } from "vitest";

describe("session loading (FIX12)", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("loads the session again when the browser comes back online", async () => {
    const listeners = new Map<string, () => void>();
    vi.stubGlobal("window", {
      addEventListener: (type: string, fn: () => void) => listeners.set(type, fn),
      removeEventListener: (type: string) => listeners.delete(type),
    });
    let online = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        if (!online) throw new TypeError("Failed to fetch");
        return new Response(
          JSON.stringify({ user: { id: "user-a", email: "a@example.com", name: null }, role: "member", authEnabled: true, signedIn: true }),
        );
      }),
    );
    const { watchSession } = await import("../src/lib/session-client");
    const seen: Array<string | null> = [];
    const stop = watchSession((session) => seen.push(session?.user?.id ?? null));
    await vi.waitFor(() => expect(seen).toEqual([null]));

    online = true;
    listeners.get("online")!();
    await vi.waitFor(() => expect(seen).toEqual([null, "user-a"]));
    stop();
    expect(listeners.has("online")).toBe(false);
  });

  it("asks past the cache when the browser comes back online and before a flush (FIX15)", async () => {
    const listeners = new Map<string, () => void>();
    vi.stubGlobal("window", {
      addEventListener: (type: string, fn: () => void) => listeners.set(type, fn),
      removeEventListener: (type: string) => listeners.delete(type),
    });
    let reply: () => Response | Promise<never> = () => sessionOf("user-a");
    const fetchMock = vi.fn(async () => reply());
    vi.stubGlobal("fetch", fetchMock);
    const { confirmSessionUser, loadSession, watchSession } = await import("../src/lib/session-client");
    const seen: Array<string | null> = [];
    const stop = watchSession((session) => seen.push(session?.user?.id ?? null));
    await vi.waitFor(() => expect(seen).toEqual(["user-a"]));

    reply = () => sessionOf("user-b");
    listeners.get("online")!();
    await vi.waitFor(() => expect(seen).toEqual(["user-a", "user-b"]));

    reply = () => sessionOf("user-c");
    expect(await confirmSessionUser()).toBe("user-c");
    expect(seen).toEqual(["user-a", "user-b", "user-c"]);
    expect((await loadSession())?.user?.id).toBe("user-c");

    reply = () => new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 });
    expect(await confirmSessionUser()).toBeNull();
    expect(seen.at(-1)).toBeNull();
    reply = () => sessionOf("user-d");
    expect((await loadSession())?.user?.id).toBe("user-d");

    reply = () => Promise.reject(new TypeError("Failed to fetch"));
    expect(await confirmSessionUser()).toBeUndefined();
    expect(seen.at(-1)).toBeNull();
    stop();
  });
});

function sessionOf(id: string): Response {
  return new Response(
    JSON.stringify({ user: { id, email: `${id}@example.com`, name: null }, role: "member", authEnabled: true, signedIn: true }),
  );
}
