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

  it("subscribes the current device, lists subscriptions, and unsubscribes cleanly", async () => {
    const p256dh = new Uint8Array(65).fill(4);
    const auth = new Uint8Array(16).fill(9);
    const endpoint = "https://push.example.test/sub/device-1";

    let currentBrowserSub: {
      endpoint: string;
      getKey: (name: "p256dh" | "auth") => ArrayBuffer;
      toJSON: () => { endpoint: string; keys: { p256dh: string; auth: string } };
      unsubscribe: () => Promise<boolean>;
    } | null = null;
    const unsubscribeMock = vi.fn(async () => {
      currentBrowserSub = null;
      return true;
    });
    const browserSub = {
      endpoint,
      getKey: (name: "p256dh" | "auth") => (name === "p256dh" ? p256dh.buffer : auth.buffer),
      toJSON: () => ({
        endpoint,
        keys: {
          p256dh: toBase64Url(p256dh),
          auth: toBase64Url(auth),
        },
      }),
      unsubscribe: unsubscribeMock,
    };

    const subscribeMock = vi.fn(async () => {
      currentBrowserSub = browserSub;
      return browserSub;
    });
    const getSubscriptionMock = vi.fn(async () => currentBrowserSub);

    const storage = new Map<string, string>();
    vi.stubGlobal("window", {
      localStorage: {
        getItem: (k: string) => storage.get(k) ?? null,
        setItem: (k: string, v: string) => {
          storage.set(k, v);
        },
        removeItem: (k: string) => {
          storage.delete(k);
        },
      },
    });
    vi.stubGlobal("Notification", {
      permission: "default",
      requestPermission: vi.fn().mockResolvedValue("granted"),
    });
    vi.stubGlobal("navigator", {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            subscribe: subscribeMock,
            getSubscription: getSubscriptionMock,
          },
        }),
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: {
            subscribe: subscribeMock,
            getSubscription: getSubscriptionMock,
          },
        }),
      },
    });

    const createdSub = {
      id: "sub-1",
      endpoint,
      deviceId: null,
      createdAt: "2026-11-14T10:00:00Z",
      updatedAt: "2026-11-14T10:00:00Z",
    };

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url === "/api/v1/my/push-subscriptions" && (!init?.method || init.method === "GET")) {
        return new Response(
          JSON.stringify({
            subscriptions: currentBrowserSub ? [createdSub] : [],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/my/push-subscriptions" && init?.method === "POST") {
        const body = JSON.parse(String(init.body ?? "{}")) as {
          endpoint: string;
          keys: { p256dh: string; auth: string };
        };
        expect(body.endpoint).toBe(endpoint);
        expect(body.keys.p256dh).toBe(toBase64Url(p256dh));
        expect(body.keys.auth).toBe(toBase64Url(auth));
        return new Response(JSON.stringify({ subscription: createdSub }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url === "/api/v1/my/push-subscriptions/sub-1" && init?.method === "DELETE") {
        return new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("not found", { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const saved = await subscribeDeviceToWebPush();
    expect(saved.id).toBe("sub-1");
    expect(subscribeMock).toHaveBeenCalledTimes(1);
    expect(await getLocalPushEndpoint()).toBe(endpoint);

    const listed = await listPushSubscriptions();
    expect(listed).toEqual([createdSub]);

    await unsubscribeDeviceFromWebPush("sub-1", endpoint);
    expect(unsubscribeMock).toHaveBeenCalledTimes(1);
    expect(await getLocalPushEndpoint()).toBeNull();
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
