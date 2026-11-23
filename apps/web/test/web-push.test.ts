import { readFileSync } from "node:fs";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toBase64Url } from "../src/lib/passkey";
import {
  getLocalPushEndpoint,
  isWebPushSupported,
  listPushSubscriptions,
  subscribeDeviceToWebPush,
  unsubscribeDeviceFromWebPush,
} from "../src/lib/web-push";
import { signOut } from "../src/lib/sign-out";

const webRoot = path.resolve(__dirname, "..");

describe("web push client helpers and service worker push handler", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("detects browser Web Push support", () => {
    vi.stubGlobal("window", {});
    expect(isWebPushSupported()).toBe(false);

    vi.stubGlobal("window", {});
    vi.stubGlobal("Notification", {
      permission: "default",
      requestPermission: vi.fn(),
    });
    vi.stubGlobal("navigator", {
      serviceWorker: {},
    });
    expect(isWebPushSupported()).toBe(true);
  });

  const serverKeyBytes = new Uint8Array(65).fill(7);
  serverKeyBytes[0] = 4;
  const serverKey = toBase64Url(serverKeyBytes);

  type FakeSub = {
    endpoint: string;
    options: { applicationServerKey: ArrayBuffer | null };
    getKey: (name: "p256dh" | "auth") => ArrayBuffer;
    toJSON: () => { endpoint: string; keys: { p256dh: string; auth: string } };
    unsubscribe: () => Promise<boolean>;
  };

  function setup(opts: { existingKey?: Uint8Array; conflictOnce?: boolean } = {}) {
    const p256dh = new Uint8Array(65).fill(4);
    const auth = new Uint8Array(16).fill(9);
    let n = 0;
    const calls: string[] = [];
    const state: { current: FakeSub | null; rows: { id: string; endpoint: string }[] } = {
      current: null,
      rows: [],
    };
    const makeSub = (key: ArrayBuffer | null): FakeSub => {
      const endpoint = `https://push.example.test/sub/device-${++n}`;
      const sub: FakeSub = {
        endpoint,
        options: { applicationServerKey: key },
        getKey: (name) => (name === "p256dh" ? p256dh.buffer : auth.buffer),
        toJSON: () => ({ endpoint, keys: { p256dh: toBase64Url(p256dh), auth: toBase64Url(auth) } }),
        unsubscribe: vi.fn(async () => {
          if (state.current === sub) state.current = null;
          calls.push(`browser-unsubscribe ${endpoint}`);
          return true;
        }),
      };
      return sub;
    };
    if (opts.existingKey) state.current = makeSub(opts.existingKey.slice().buffer);
    const subscribeMock = vi.fn(async (o: PushSubscriptionOptionsInit) => {
      const key = o.applicationServerKey as Uint8Array;
      state.current = makeSub(key.slice().buffer);
      return state.current;
    });
    const pushManager = { subscribe: subscribeMock, getSubscription: vi.fn(async () => state.current) };

    const storage = new Map<string, string>();
    vi.stubGlobal("window", {
      location: { href: "/" },
      localStorage: {
        getItem: (k: string) => storage.get(k) ?? null,
        setItem: (k: string, v: string) => void storage.set(k, v),
        removeItem: (k: string) => void storage.delete(k),
      },
    });
    vi.stubGlobal("Notification", { permission: "default", requestPermission: vi.fn().mockResolvedValue("granted") });
    vi.stubGlobal("navigator", {
      serviceWorker: { ready: Promise.resolve({ pushManager }), getRegistration: vi.fn().mockResolvedValue({ pushManager }) },
    });

    let conflict = opts.conflictOnce ?? false;
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input.toString();
        const method = init?.method ?? "GET";
        calls.push(`${method} ${url}`);
        if (url === "/api/v1/my/push-subscriptions" && method === "GET") {
          const subscriptions = state.rows.map((r) => ({
            ...r, deviceId: null, createdAt: "2026-11-15T10:00:00Z", updatedAt: "2026-11-15T10:00:00Z",
          }));
          return json({ subscriptions, vapidPublicKey: serverKey });
        }
        if (url === "/api/v1/my/push-subscriptions" && method === "POST") {
          const body = JSON.parse(String(init?.body ?? "{}")) as { endpoint: string; keys: { p256dh: string; auth: string } };
          expect(body.keys).toEqual({ p256dh: toBase64Url(p256dh), auth: toBase64Url(auth) });
          if (conflict) {
            conflict = false;
            return json({ error: "taken", code: "push_endpoint_taken" }, 409);
          }
          const row = { id: `sub-${state.rows.length + 1}`, endpoint: body.endpoint };
          state.rows.push(row);
          return json({
            subscription: { ...row, deviceId: null, createdAt: "2026-11-15T10:00:00Z", updatedAt: "2026-11-15T10:00:00Z" },
          });
        }
        const del = url.match(/^\/api\/v1\/my\/push-subscriptions\/(.+)$/);
        if (del && method === "DELETE") {
          state.rows = state.rows.filter((r) => r.id !== del[1]);
          return json({ ok: true });
        }
        if (url === "/api/v1/auth/logout") return json({ ok: true });
        return new Response("not found", { status: 404 });
      }),
    );
    return { state, calls, subscribeMock };
  }

  it("subscribes with the server's VAPID key, lists, and unsubscribes cleanly", async () => {
    const { state, subscribeMock } = setup();
    const saved = await subscribeDeviceToWebPush();
    expect(saved.id).toBe("sub-1");
    expect(subscribeMock).toHaveBeenCalledTimes(1);
    expect(toBase64Url(subscribeMock.mock.calls[0]![0].applicationServerKey as Uint8Array)).toBe(serverKey);
    expect(await getLocalPushEndpoint()).toBe(saved.endpoint);
    expect((await listPushSubscriptions()).map((s) => s.id)).toEqual(["sub-1"]);

    await unsubscribeDeviceFromWebPush("sub-1", saved.endpoint);
    expect(state.current).toBeNull();
    expect(await getLocalPushEndpoint()).toBeNull();
  });

  it("replaces a browser subscription made with another key", async () => {
    const { calls, subscribeMock } = setup({ existingKey: new Uint8Array(65).fill(1) });
    await subscribeDeviceToWebPush();
    expect(calls).toContain("browser-unsubscribe https://push.example.test/sub/device-1");
    expect(subscribeMock).toHaveBeenCalledTimes(1);
  });

  it("subscribes again for a new endpoint when another user owns this one", async () => {
    const { state, calls, subscribeMock } = setup({ existingKey: serverKeyBytes, conflictOnce: true });
    const saved = await subscribeDeviceToWebPush();
    expect(calls).toContain("browser-unsubscribe https://push.example.test/sub/device-1");
    expect(subscribeMock).toHaveBeenCalledTimes(1);
    expect(saved.endpoint).toBe("https://push.example.test/sub/device-2");
    expect(state.rows.map((r) => r.endpoint)).toEqual(["https://push.example.test/sub/device-2"]);
  });

  it("removes this browser's subscription before sign-out ends the session", async () => {
    const { state, calls } = setup();
    await subscribeDeviceToWebPush();
    await signOut();
    expect(state.rows).toEqual([]);
    expect(state.current).toBeNull();
    expect(await getLocalPushEndpoint()).toBeNull();
    const del = calls.indexOf("DELETE /api/v1/my/push-subscriptions/sub-1");
    const logout = calls.indexOf("POST /api/v1/auth/logout");
    expect(del).toBeGreaterThan(-1);
    expect(logout).toBeGreaterThan(del);
    expect((window as unknown as { location: { href: string } }).location.href).toBe("/signin");
  });

  it("maps the three fixed Web Push events in sw.js without reading any run or business fields", () => {
    const sw = readFileSync(path.join(webRoot, "public/sw.js"), "utf8");
    expect(sw).toContain("approval_waiting:");
    expect(sw).toContain('body: "An approval is waiting."');
    expect(sw).toContain('url: "/actions"');
    expect(sw).toContain("run_finished:");
    expect(sw).toContain('body: "A run finished."');
    expect(sw).toContain('url: "/runs"');
    expect(sw).toContain("workflow_needs_attention:");
    expect(sw).toContain('body: "A workflow needs attention."');
    expect(sw).toContain('url: "/workflows"');
    expect(sw).toContain('self.addEventListener("push"');
    expect(sw).toContain('self.addEventListener("notificationclick"');
  });
});
