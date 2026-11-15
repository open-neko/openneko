"use client";

import { useSyncExternalStore } from "react";

export const OFFLINE_QUEUE_STORAGE_PREFIX = "openneko.work.offlineQueue";
export const OFFLINE_QUEUE_USER_KEY = "openneko.work.offlineQueueUser";
const OFFLINE_QUEUE_LOCK = "openneko.work.offlineQueue.flush";

export type QueuedQuestion = {
  id: string;
  threadId: string | null;
  threadBase: "/work" | "/chat/t";
  content: string;
  createdAt: string;
  status: "waiting" | "sending";
};

const EMPTY_QUEUE: readonly QueuedQuestion[] = [];
const listeners = new Set<() => void>();
const ownSending = new Set<string>();
let initialized = false;
let userId: string | null = null;
let memoryQueue: QueuedQuestion[] = [];
let queue: readonly QueuedQuestion[] = EMPTY_QUEUE;
let nextCounter = 1;

function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage ?? null;
  } catch {
    return null;
  }
}

function storageKey(): string | null {
  return userId ? `${OFFLINE_QUEUE_STORAGE_PREFIX}.user.${userId}` : null;
}

function parseQueue(raw: string | null): QueuedQuestion[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    const items: QueuedQuestion[] = [];
    for (const entry of parsed) {
      if (!entry || typeof entry !== "object") continue;
      const candidate = entry as Partial<QueuedQuestion>;
      if (
        typeof candidate.id === "string" &&
        typeof candidate.content === "string" &&
        candidate.content.trim().length > 0 &&
        (candidate.threadBase === "/work" || candidate.threadBase === "/chat/t") &&
        (candidate.threadId === null || typeof candidate.threadId === "string") &&
        typeof candidate.createdAt === "string"
      ) {
        items.push({
          id: candidate.id,
          threadId: candidate.threadId,
          threadBase: candidate.threadBase,
          content: candidate.content,
          createdAt: candidate.createdAt,
          status: candidate.status === "sending" ? "sending" : "waiting",
        });
      }
    }
    return items;
  } catch {
    return [];
  }
}

function readItems(): QueuedQuestion[] {
  const store = storage();
  const key = storageKey();
  if (!store || !key) return memoryQueue;
  try {
    return parseQueue(store.getItem(key));
  } catch {
    return memoryQueue;
  }
}

function writeItems(items: QueuedQuestion[]) {
  memoryQueue = items;
  const store = storage();
  const key = storageKey();
  if (store && key) {
    try {
      if (items.length === 0) store.removeItem(key);
      else store.setItem(key, JSON.stringify(items));
    } catch {
      // The in-memory queue still works when storage is full or blocked.
    }
  }
  refresh(items);
}

function refresh(items = readItems()) {
  const next = items.map((item) => ({
    ...item,
    status: ownSending.has(item.id) ? ("sending" as const) : ("waiting" as const),
  }));
  const same =
    next.length === queue.length &&
    next.every(
      (item, i) =>
        item.id === queue[i].id &&
        item.status === queue[i].status &&
        item.threadId === queue[i].threadId,
    );
  if (same) return;
  queue = next.length === 0 ? EMPTY_QUEUE : next;
  for (const listener of listeners) listener();
}

function onStorage(event: StorageEvent) {
  if (event.key === null || event.key === storageKey()) refresh();
}

function ensureInitialized() {
  if (initialized || typeof window === "undefined") return;
  initialized = true;
  const store = storage();
  try {
    store?.removeItem(OFFLINE_QUEUE_STORAGE_PREFIX);
    userId = store?.getItem(OFFLINE_QUEUE_USER_KEY) ?? null;
  } catch {
    userId = null;
  }
  window.addEventListener?.("storage", onStorage);
  refresh();
}

/** Selects the queue of the signed-in user; each user has a storage key of their own. */
export function setOfflineQueueUser(id: string | null): void {
  ensureInitialized();
  if (!id || id === userId) return;
  const pending = storageKey() ? [] : memoryQueue;
  userId = id;
  try {
    storage()?.setItem(OFFLINE_QUEUE_USER_KEY, id);
  } catch {
    // Storage is optional.
  }
  memoryQueue = [];
  writeItems([...readItems(), ...pending]);
}

/** Removes every stored queue and the remembered user. Sign-out calls it. */
export function clearOfflineQueues(): void {
  const store = storage();
  if (store) {
    try {
      const keys: string[] = [];
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i);
        if (key?.startsWith(OFFLINE_QUEUE_STORAGE_PREFIX)) keys.push(key);
      }
      for (const key of keys) store.removeItem(key);
    } catch {
      // Storage is optional.
    }
  }
  userId = null;
  memoryQueue = [];
  ownSending.clear();
  refresh([]);
}

/** Runs fn while no other tab of this browser flushes the queue. */
export async function withOfflineQueueLock<T>(fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator === "undefined" ? undefined : navigator.locks;
  if (!locks) return fn();
  return locks.request(OFFLINE_QUEUE_LOCK, fn) as Promise<T>;
}

export function newClientRequestId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `offline-${Date.now()}-${Math.random().toString(36).slice(2)}-${nextCounter++}`;
}

export function getOfflineQueueSnapshot(): readonly QueuedQuestion[] {
  ensureInitialized();
  return queue;
}

export function subscribeOfflineQueue(listener: () => void): () => void {
  ensureInitialized();
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useOfflineQueue(): readonly QueuedQuestion[] {
  return useSyncExternalStore(
    subscribeOfflineQueue,
    getOfflineQueueSnapshot,
    () => EMPTY_QUEUE,
  );
}

export function enqueueOfflineQuestion(input: {
  id?: string;
  threadId: string | null;
  threadBase: "/work" | "/chat/t";
  content: string;
  createdAt?: string;
}): QueuedQuestion {
  ensureInitialized();
  const items = readItems();
  const id = input.id ?? newClientRequestId();
  ownSending.delete(id);
  const existing = items.find((item) => item.id === id);
  if (existing) {
    writeItems(items.map((item) => (item.id === id ? { ...item, status: "waiting" } : item)));
    return { ...existing, status: "waiting" };
  }
  const item: QueuedQuestion = {
    id,
    threadId: input.threadId,
    threadBase: input.threadBase,
    content: input.content,
    createdAt: input.createdAt ?? new Date().toISOString(),
    status: "waiting",
  };
  writeItems([...items, item]);
  return item;
}

export function cancelOfflineQuestion(id: string): boolean {
  ensureInitialized();
  if (ownSending.has(id)) return false;
  const items = readItems();
  if (!items.some((item) => item.id === id)) return false;
  writeItems(items.filter((item) => item.id !== id));
  return true;
}

/**
 * Marks the next question of the thread as sending, in storage too, so a
 * reload keeps it. Call it inside withOfflineQueueLock: the lock is what
 * makes a stored "sending" item of a closed tab safe to claim again.
 */
export function claimNextOfflineQuestion(
  threadId: string | null,
  threadBase: "/work" | "/chat/t",
): QueuedQuestion | null {
  ensureInitialized();
  const items = readItems();
  const index = items.findIndex(
    (item) =>
      !ownSending.has(item.id) &&
      item.threadBase === threadBase &&
      item.threadId === threadId,
  );
  if (index === -1) return null;
  const claimed: QueuedQuestion = { ...items[index], status: "sending" };
  ownSending.add(claimed.id);
  writeItems(items.map((item, i) => (i === index ? claimed : item)));
  return claimed;
}

export function completeOfflineQuestion(id: string): void {
  ensureInitialized();
  ownSending.delete(id);
  const items = readItems();
  if (!items.some((item) => item.id === id)) {
    refresh(items);
    return;
  }
  writeItems(items.filter((item) => item.id !== id));
}

export function requeueOfflineQuestion(item: QueuedQuestion): void {
  ensureInitialized();
  ownSending.delete(item.id);
  const waitingItem: QueuedQuestion = { ...item, status: "waiting" };
  const items = readItems();
  const index = items.findIndex((existing) => existing.id === item.id);
  writeItems(
    index >= 0
      ? items.map((existing, i) => (i === index ? waitingItem : existing))
      : [waitingItem, ...items],
  );
}

export function resetOfflineQueueForTests(): void {
  if (initialized && typeof window !== "undefined") {
    window.removeEventListener?.("storage", onStorage);
  }
  initialized = false;
  userId = null;
  memoryQueue = [];
  ownSending.clear();
  queue = EMPTY_QUEUE;
  listeners.clear();
}

export function clearOfflineQueueForTests(): void {
  ownSending.clear();
  writeItems([]);
}
