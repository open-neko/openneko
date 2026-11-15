"use client";

import { useSyncExternalStore } from "react";

export const OFFLINE_QUEUE_STORAGE_PREFIX = "openneko.work.offlineQueue";
const LEGACY_USER_KEY = "openneko.work.offlineQueueUser";
const OFFLINE_QUEUE_LOCK = "openneko.work.offlineQueue.flush";
// A claim of another tab blocks a claim for this long when the browser has
// no Web Locks; the server matches a repeat by its client request id.
export const OFFLINE_CLAIM_LEASE_MS = 60_000;

export type QueuedQuestion = {
  id: string;
  threadId: string | null;
  threadBase: "/work" | "/chat/t";
  content: string;
  createdAt: string;
  status: "waiting" | "sending" | "failed";
  claimedBy?: string;
  claimedAt?: number;
  retryAt?: number;
  error?: string;
};

const EMPTY_QUEUE: readonly QueuedQuestion[] = [];
const listeners = new Set<() => void>();
const ownSending = new Set<string>();
let initialized = false;
let userId: string | null = null;
let memoryQueue: QueuedQuestion[] = [];
let queue: readonly QueuedQuestion[] = EMPTY_QUEUE;
let nextCounter = 1;
let lockDepth = 0;

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
          status:
            candidate.status === "sending" || candidate.status === "failed"
              ? candidate.status
              : "waiting",
          ...(typeof candidate.claimedBy === "string" ? { claimedBy: candidate.claimedBy } : {}),
          ...(typeof candidate.claimedAt === "number" ? { claimedAt: candidate.claimedAt } : {}),
          ...(typeof candidate.retryAt === "number" ? { retryAt: candidate.retryAt } : {}),
          ...(typeof candidate.error === "string" ? { error: candidate.error } : {}),
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
    status: ownSending.has(item.id)
      ? ("sending" as const)
      : item.status === "failed"
        ? ("failed" as const)
        : ("waiting" as const),
  }));
  const same =
    next.length === queue.length &&
    next.every(
      (item, i) =>
        item.id === queue[i].id &&
        item.status === queue[i].status &&
        item.threadId === queue[i].threadId &&
        item.retryAt === queue[i].retryAt &&
        item.error === queue[i].error,
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
    store?.removeItem(LEGACY_USER_KEY);
  } catch {
    // Storage is optional.
  }
  window.addEventListener?.("storage", onStorage);
  refresh();
}

/**
 * Selects the queue of the signed-in user. Until the session names the user,
 * the queue holds only what this page typed, and nothing sends.
 */
export function setOfflineQueueUser(id: string | null): void {
  ensureInitialized();
  if (id === userId) return;
  ownSending.clear();
  if (!id) {
    userId = null;
    memoryQueue = [];
    refresh([]);
    return;
  }
  const pending = userId ? [] : memoryQueue;
  userId = id;
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

/**
 * Runs fn while no other tab of this browser flushes the queue. With no Web
 * Locks, fn runs at once and the claim stored on each question guards it.
 */
export async function withOfflineQueueLock<T>(fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator === "undefined" ? undefined : navigator.locks;
  if (!locks) return fn();
  return locks.request(OFFLINE_QUEUE_LOCK, async () => {
    lockDepth++;
    try {
      return await fn();
    } finally {
      lockDepth--;
    }
  }) as Promise<T>;
}

const TAB_ID = newClientRequestId();

export function newClientRequestId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `offline-${Date.now()}-${Math.random().toString(36).slice(2)}-${nextCounter++}`;
}

/**
 * A queued question completes on 2xx only; 429 and 5xx send it again later.
 * A 401 keeps it for its user to send after the next sign-in.
 */
export function offlineSendOutcome(status: number): "done" | "retry" | "signin" | "refused" {
  if (status >= 200 && status < 300) return "done";
  if (status === 429 || status >= 500) return "retry";
  if (status === 401) return "signin";
  return "refused";
}

export function offlineRetryDelayMs(res: { status: number; headers: Headers }): number {
  const seconds = Number(res.headers.get("Retry-After"));
  if (Number.isFinite(seconds) && seconds > 0) return Math.min(Math.max(seconds, 5), 300) * 1000;
  return res.status === 429 ? 30_000 : 15_000;
}

/**
 * Applies a non-2xx answer to a claimed question: it waits for a later send,
 * or it keeps the server's refusal and does not send again.
 */
export function settleOfflineRefusal(
  id: string,
  res: { status: number; headers: Headers },
  error: string,
  threadId?: string,
): "retry" | "signin" | "refused" {
  const outcome = offlineSendOutcome(res.status);
  if (outcome === "done") throw new Error(`HTTP ${res.status} is not a refusal`);
  if (outcome === "refused") {
    failOfflineQuestion(id, `The server refused this question (HTTP ${res.status}): ${error}`, threadId);
  } else {
    deferOfflineQuestion(id, Date.now() + offlineRetryDelayMs(res), threadId);
  }
  return outcome;
}

export function offlineQueueUser(): string | null {
  return userId;
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
    const waiting: QueuedQuestion = { ...released(existing), status: "waiting" };
    writeItems(items.map((item) => (item.id === id ? waiting : item)));
    return waiting;
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
 * Marks the next question of the thread as sending by this tab, in storage
 * too, so a reload keeps it. Inside withOfflineQueueLock the lock makes the
 * claim of a closed tab safe to take; with no lock, a claim of another tab
 * holds until it is OFFLINE_CLAIM_LEASE_MS old.
 */
export function claimNextOfflineQuestion(
  threadId: string | null,
  threadBase: "/work" | "/chat/t",
): QueuedQuestion | null {
  ensureInitialized();
  if (!userId) return null;
  const now = Date.now();
  const items = readItems();
  const index = items.findIndex(
    (item) =>
      !ownSending.has(item.id) &&
      item.status !== "failed" &&
      item.threadBase === threadBase &&
      item.threadId === threadId &&
      !(item.retryAt && item.retryAt > now) &&
      (lockDepth > 0 ||
        item.status !== "sending" ||
        !item.claimedBy ||
        item.claimedBy === TAB_ID ||
        now - (item.claimedAt ?? 0) > OFFLINE_CLAIM_LEASE_MS),
  );
  if (index === -1) return null;
  const claimed: QueuedQuestion = {
    ...items[index],
    status: "sending",
    claimedBy: TAB_ID,
    claimedAt: now,
  };
  delete claimed.retryAt;
  ownSending.add(claimed.id);
  writeItems(items.map((item, i) => (i === index ? claimed : item)));
  if (readItems().find((item) => item.id === claimed.id)?.claimedBy !== TAB_ID) {
    ownSending.delete(claimed.id);
    refresh();
    return null;
  }
  return claimed;
}

function updateOfflineQuestion(id: string, change: (item: QueuedQuestion) => QueuedQuestion) {
  ensureInitialized();
  ownSending.delete(id);
  const items = readItems();
  if (!items.some((item) => item.id === id)) {
    refresh(items);
    return;
  }
  writeItems(items.map((item) => (item.id === id ? change(item) : item)));
}

function released(item: QueuedQuestion): QueuedQuestion {
  const next = { ...item };
  delete next.claimedBy;
  delete next.claimedAt;
  delete next.retryAt;
  delete next.error;
  return next;
}

/** Keeps the question to send again at retryAt, for a busy or failing server. */
export function deferOfflineQuestion(id: string, retryAt: number, threadId?: string): void {
  updateOfflineQuestion(id, (item) => ({
    ...released(item),
    status: "waiting",
    retryAt,
    ...(threadId ? { threadId } : {}),
  }));
}

/** Keeps the question with the server's refusal; it does not send again. */
export function failOfflineQuestion(id: string, error: string, threadId?: string): void {
  updateOfflineQuestion(id, (item) => ({
    ...released(item),
    status: "failed",
    error,
    ...(threadId ? { threadId } : {}),
  }));
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
  const waitingItem: QueuedQuestion = { ...released(item), status: "waiting" };
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
  lockDepth = 0;
  ownSending.clear();
  queue = EMPTY_QUEUE;
  listeners.clear();
}

export function clearOfflineQueueForTests(): void {
  ownSending.clear();
  writeItems([]);
}
