import { clearOfflineQueues } from "@/app/(work)/work/work-offline-queue";
import { getLocalPushEndpoint, unsubscribeThisBrowserFromWebPush } from "./web-push";

const PUSH_CLEANUP_LIMIT_MS = 3000;

function within<T>(work: Promise<T>, fallback: T): Promise<T> {
  return Promise.race([
    work.catch(() => fallback),
    new Promise<T>((resolve) => setTimeout(() => resolve(fallback), PUSH_CLEANUP_LIMIT_MS)),
  ]);
}

export async function signOut(): Promise<void> {
  clearOfflineQueues();
  try {
    const pushEndpoint = await within(getLocalPushEndpoint(), null);
    await within(unsubscribeThisBrowserFromWebPush(), undefined);
    await fetch("/api/v1/auth/logout", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(pushEndpoint ? { pushEndpoint } : {}),
    });
  } finally {
    window.location.href = "/signin";
  }
}
