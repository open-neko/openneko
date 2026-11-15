import { fromBase64Url, toBase64Url } from "./passkey";

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

async function loadPushState(): Promise<{
  subscriptions: PushSubscriptionSummary[];
  vapidPublicKey: string;
}> {
  const res = await fetch("/api/v1/my/push-subscriptions", { cache: "no-store" });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not load push subscriptions (${res.status}).`);
  }
  const body = (await res.json()) as {
    subscriptions?: PushSubscriptionSummary[];
    vapidPublicKey?: string;
  };
  return { subscriptions: body.subscriptions ?? [], vapidPublicKey: body.vapidPublicKey ?? "" };
}

export async function listPushSubscriptions(): Promise<PushSubscriptionSummary[]> {
  return (await loadPushState()).subscriptions;
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

function sameKey(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false;
  const left = new Uint8Array(a);
  return left.length === b.length && left.every((v, i) => v === b[i]);
}

async function browserSubscription(
  registration: ServiceWorkerRegistration,
  serverKey: Uint8Array<ArrayBuffer>,
  fresh: boolean,
): Promise<PushSubscription> {
  const existing = await registration.pushManager.getSubscription();
  if (existing && !fresh && sameKey(existing.options?.applicationServerKey, serverKey)) {
    return existing;
  }
  if (existing) await existing.unsubscribe();
  return await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: serverKey,
  });
}

async function postSubscription(sub: PushSubscription): Promise<Response> {
  const { endpoint, p256dh, auth } = extractSubscriptionKeys(sub);
  return await fetch("/api/v1/my/push-subscriptions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ endpoint, keys: { p256dh, auth } }),
  });
}

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
  const { vapidPublicKey } = await loadPushState();
  if (!vapidPublicKey) {
    throw new Error("The server did not give its push key.");
  }
  const serverKey = fromBase64Url(vapidPublicKey);
  let res = await postSubscription(await browserSubscription(registration, serverKey, false));
  if (res.status === 409) {
    res = await postSubscription(await browserSubscription(registration, serverKey, true));
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `Could not save push subscription (${res.status}).`);
  }
  const body = (await res.json()) as { subscription: PushSubscriptionSummary };
  writeStoredEndpoint(body.subscription.endpoint);
  return body.subscription;
}

// Sign-out calls this before the session ends, so the next user of this
// browser gets no notice meant for the user who left.
export async function unsubscribeThisBrowserFromWebPush(): Promise<void> {
  const endpoint = await getLocalPushEndpoint();
  if (!endpoint) return;
  try {
    const { subscriptions } = await loadPushState();
    const mine = subscriptions.find((s) => s.endpoint === endpoint);
    if (mine) {
      await fetch(`/api/v1/my/push-subscriptions/${encodeURIComponent(mine.id)}`, {
        method: "DELETE",
      });
    }
  } finally {
    writeStoredEndpoint(null);
    if (isWebPushSupported()) {
      const reg = await navigator.serviceWorker.getRegistration("/").catch(() => undefined);
      const sub = await reg?.pushManager?.getSubscription().catch(() => null);
      await sub?.unsubscribe().catch(() => false);
    }
  }
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
