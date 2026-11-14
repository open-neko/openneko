import { toBase64Url } from "./passkey";

export type PushSubscriptionSummary = {
  id: string;
  endpoint: string;
  deviceId: string | null;
  createdAt: string;
  updatedAt: string;
};

const LOCAL_ENDPOINT_STORAGE_KEY = "openneko.push.endpoint";

export function isWebPushSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    typeof Notification !== "undefined"
  );
}

function readStoredEndpoint(): string | null {
  try {
    return window.localStorage.getItem(LOCAL_ENDPOINT_STORAGE_KEY);
  } catch {
    return null;
  }
}

function writeStoredEndpoint(endpoint: string | null): void {
  try {
    if (endpoint) {
      window.localStorage.setItem(LOCAL_ENDPOINT_STORAGE_KEY, endpoint);
    } else {
      window.localStorage.removeItem(LOCAL_ENDPOINT_STORAGE_KEY);
    }
  } catch {
    // Ignore storage failures in restricted contexts.
  }
}

async function activeServiceWorkerRegistration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration("/");
  if (existing?.pushManager) {
    return existing;
  }
  const registered = await navigator.serviceWorker.register("/sw.js", {
    scope: "/",
    updateViaCache: "none",
  });
  if (registered.pushManager) {
    return registered;
  }
  return await navigator.serviceWorker.ready;
}

export async function getLocalPushEndpoint(): Promise<string | null> {
  if (!isWebPushSupported()) return readStoredEndpoint();
  try {
    const reg = await navigator.serviceWorker.getRegistration("/");
    const sub = await reg?.pushManager?.getSubscription();
    if (sub?.endpoint) {
      writeStoredEndpoint(sub.endpoint);
      return sub.endpoint;
    }
  } catch {
    // Fall back to stored endpoint.
  }
  return readStoredEndpoint();
}

export async function listPushSubscriptions(): Promise<PushSubscriptionSummary[]> {
  const res = await fetch("/api/v1/my/push-subscriptions", { cache: "no-store" });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not load push subscriptions (${res.status}).`);
  }
  const body = (await res.json()) as { subscriptions?: PushSubscriptionSummary[] };
  return body.subscriptions ?? [];
}

function extractSubscriptionKeys(sub: PushSubscription): {
  endpoint: string;
  p256dh: string;
  auth: string;
} {
  const json = typeof sub.toJSON === "function" ? sub.toJSON() : null;
  let p256dh = json?.keys?.p256dh ?? "";
  let auth = json?.keys?.auth ?? "";
  if (!p256dh && typeof sub.getKey === "function") {
    const key = sub.getKey("p256dh");
    if (key) p256dh = toBase64Url(key);
  }
  if (!auth && typeof sub.getKey === "function") {
    const key = sub.getKey("auth");
    if (key) auth = toBase64Url(key);
  }
  return {
    endpoint: sub.endpoint,
    p256dh,
    auth,
  };
}

// Uncompressed P-256 public point fallback for browsers that require an
// applicationServerKey argument on PushManager.subscribe.
const FALLBACK_APPLICATION_SERVER_KEY =
  "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U";

export async function subscribeDeviceToWebPush(): Promise<PushSubscriptionSummary> {
  if (!isWebPushSupported()) {
    throw new Error("Push notifications are not supported in this browser.");
  }
  const permission =
    Notification.permission === "granted"
      ? "granted"
      : await Notification.requestPermission();
  if (permission !== "granted") {
    throw new Error("Notification permission was not granted.");
  }
  const registration = await activeServiceWorkerRegistration();
  if (!registration.pushManager) {
    throw new Error("Push notifications are not available on this device.");
  }
  let browserSub = await registration.pushManager.getSubscription();
  if (!browserSub) {
    try {
      browserSub = await registration.pushManager.subscribe({
        userVisibleOnly: true,
      });
    } catch {
      const { fromBase64Url } = await import("./passkey");
      browserSub = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: fromBase64Url(FALLBACK_APPLICATION_SERVER_KEY),
      });
    }
  }
  const { endpoint, p256dh, auth } = extractSubscriptionKeys(browserSub);
  const res = await fetch("/api/v1/my/push-subscriptions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      endpoint,
      keys: { p256dh, auth },
    }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not save push subscription (${res.status}).`);
  }
  const body = (await res.json()) as { subscription: PushSubscriptionSummary };
  writeStoredEndpoint(body.subscription.endpoint);
  return body.subscription;
}

export async function unsubscribeDeviceFromWebPush(
  subscriptionId: string,
  endpoint?: string,
): Promise<void> {
  const res = await fetch(
    `/api/v1/my/push-subscriptions/${encodeURIComponent(subscriptionId)}`,
    { method: "DELETE" },
  );
  if (!res.ok && res.status !== 404) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not remove push subscription (${res.status}).`);
  }
  const localEndpoint = await getLocalPushEndpoint();
  if (!endpoint || endpoint === localEndpoint) {
    writeStoredEndpoint(null);
    if (isWebPushSupported()) {
      try {
        const reg = await navigator.serviceWorker.getRegistration("/");
        const sub = await reg?.pushManager?.getSubscription();
        if (sub && (!endpoint || sub.endpoint === endpoint)) {
          await sub.unsubscribe();
        }
      } catch {
        // Ignore browser unsubscribe errors after server deletion.
      }
    }
  }
}
