import { unsubscribeThisBrowserFromWebPush } from "./web-push";

const PUSH_CLEANUP_LIMIT_MS = 3000;

export async function signOut(): Promise<void> {
  try {
    await Promise.race([
      unsubscribeThisBrowserFromWebPush().catch(() => undefined),
      new Promise((resolve) => setTimeout(resolve, PUSH_CLEANUP_LIMIT_MS)),
    ]);
    await fetch("/api/v1/auth/logout", { method: "POST" });
  } finally {
    window.location.href = "/signin";
  }
}
