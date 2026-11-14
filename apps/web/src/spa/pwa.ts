import { useSyncExternalStore } from "react";

let networkReachable = true;
const listeners = new Set<() => void>();
let listeningWindow = false;

function notify() {
  for (const listener of listeners) listener();
}

function ensureWindowListeners() {
  if (listeningWindow || typeof window === "undefined") return;
  listeningWindow = true;
  window.addEventListener("online", () => {
    networkReachable = true;
    notify();
    window.dispatchEvent(new Event("openneko:refresh"));
  });
  window.addEventListener("offline", () => {
    notify();
  });
}

export function isBrowserOnline(): boolean {
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    return false;
  }
  return networkReachable;
}

export function reportNetworkOffline(): void {
  if (!networkReachable) return;
  networkReachable = false;
  notify();
}

export function reportNetworkOnline(): void {
  if (networkReachable) return;
  networkReachable = true;
  notify();
}

export function subscribeOnlineStatus(listener: () => void): () => void {
  ensureWindowListeners();
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useOnlineStatus(): boolean {
  return useSyncExternalStore(subscribeOnlineStatus, isBrowserOnline, () => true);
}

function waitForActivation(worker: ServiceWorker): Promise<void> {
  if (worker.state === "activated" || worker.state === "redundant") {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const onState = () => {
      if (worker.state === "activated" || worker.state === "redundant") {
        worker.removeEventListener("statechange", onState);
        resolve();
      }
    };
    worker.addEventListener("statechange", onState);
  });
}

export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (typeof window === "undefined" || !("serviceWorker" in navigator)) {
    return null;
  }
  ensureWindowListeners();
  try {
    const registration = await navigator.serviceWorker.register("/sw.js", {
      scope: "/",
      updateViaCache: "none",
    });
    await registration.update().catch(() => undefined);
    const pending = registration.installing ?? registration.waiting;
    if (pending) {
      await waitForActivation(pending);
    }
    return registration;
  } catch {
    return null;
  }
}
