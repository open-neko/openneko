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
});
