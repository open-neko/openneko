"use client";

import { useSyncExternalStore } from "react";

export const OFFLINE_QUEUE_STORAGE_PREFIX = "openneko.work.offlineQueue";
const USER_KEY_PREFIX = `${OFFLINE_QUEUE_STORAGE_PREFIX}.user.`;
const PENDING_KEY = `${OFFLINE_QUEUE_STORAGE_PREFIX}.pending`;
const LEGACY_USER_KEY = "openneko.work.offlineQueueUser";
const OFFLINE_QUEUE_LOCK = "openneko.work.offlineQueue.flush";
export const OFFLINE_QUESTION_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
export const OFFLINE_RETRY_MAX_MS = 300_000;
export const OFFLINE_NETWORK_RETRY_MAX_MS = 30_000;
export const OFFLINE_SEND_TIMEOUT_MS = 30_000;

export type QueuedQuestion = {
  id: string;
  threadId: string | null;
  threadBase: "/work" | "/chat/t";
  content: string;
  createdAt: string;
  status: "waiting" | "sending" | "failed";
  attempts?: number;
  retryAt?: number;
  error?: string;
};

const EMPTY_QUEUE: readonly QueuedQuestion[] = [];
const listeners = new Set<() => void>();
const ownSending = new Set<string>();
let initialized = false;
let userId: string | null = null;
let memoryQueue: QueuedQuestion[] = [];
let storageBroken = false;
let signedOutHere = false;
let queue: readonly QueuedQuestion[] = EMPTY_QUEUE;
let nextCounter = 1;

function webStorage(kind: "localStorage" | "sessionStorage"): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window[kind] ?? null;
  } catch {
    return null;
  }
}

// Before the session names the user, a question goes to this tab's session
// storage, so only the first user confirmed in this tab can take it.
function currentStore(): { store: Storage | null; key: string; memoryOnly?: boolean } {
  if (userId) return { store: webStorage("localStorage"), key: `${USER_KEY_PREFIX}${userId}` };
  if (signedOutHere) return { store: null, key: PENDING_KEY, memoryOnly: true };
  return { store: webStorage("sessionStorage"), key: PENDING_KEY };
}

function parseQueue(raw: string | null): QueuedQuestion[] {
  if (!raw) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  const now = Date.now();
  const items: QueuedQuestion[] = [];
  for (const entry of parsed) {
    if (!entry || typeof entry !== "object") continue;
    const candidate = entry as Partial<QueuedQuestion>;
    if (
      typeof candidate.id !== "string" ||
      typeof candidate.content !== "string" ||
      candidate.content.trim().length === 0 ||
      (candidate.threadBase !== "/work" && candidate.threadBase !== "/chat/t") ||
      (candidate.threadId !== null && typeof candidate.threadId !== "string") ||
      typeof candidate.createdAt !== "string"
    ) {
      continue;
    }
    const created = Date.parse(candidate.createdAt);
    if (!Number.isFinite(created) || now - created > OFFLINE_QUESTION_MAX_AGE_MS) continue;
    items.push({
      id: candidate.id,
      threadId: candidate.threadId,
      threadBase: candidate.threadBase,
      content: candidate.content,
      createdAt: candidate.createdAt,
      status: candidate.status === "failed" ? "failed" : "waiting",
      ...(typeof candidate.attempts === "number" && candidate.attempts > 0
        ? { attempts: Math.min(Math.floor(candidate.attempts), 30) }
        : {}),
      ...(typeof candidate.retryAt === "number"
        ? { retryAt: Math.min(candidate.retryAt, now + OFFLINE_RETRY_MAX_MS) }
        : {}),
      ...(typeof candidate.error === "string" ? { error: candidate.error } : {}),
    });
  }
  return items;
}

function readItems(): QueuedQuestion[] {
  if (storageBroken) return memoryQueue;
  const { store, key, memoryOnly } = currentStore();
  if (!store || memoryOnly) return memoryQueue;
  try {
    return parseQueue(store.getItem(key));
  } catch {
    return memoryQueue;
  }
}

function writeItems(items: QueuedQuestion[]) {
  memoryQueue = items;
  const { store, key, memoryOnly } = currentStore();
  let saved = memoryOnly === true;
  if (store) {
    try {
      if (items.length === 0) store.removeItem(key);
      else store.setItem(key, JSON.stringify(items));
      saved = true;
    } catch {
      saved = false;
    }
  }
  setStorageBroken(!saved && items.length > 0);
  refresh(items);
}

function setStorageBroken(next: boolean) {
  if (storageBroken === next) return;
  storageBroken = next;
  for (const listener of listeners) listener();
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
  if (event.key === null || event.key === currentStore().key) refresh();
}

function ensureInitialized() {
  if (initialized || typeof window === "undefined") return;
  initialized = true;
  const store = webStorage("localStorage");
  try {
    store?.removeItem(OFFLINE_QUEUE_STORAGE_PREFIX);
    store?.removeItem(LEGACY_USER_KEY);
  } catch {
    // Storage is optional.
  }
  window.addEventListener?.("storage", onStorage);
  refresh();
}

function removeKeys(store: Storage | null, match: (key: string) => boolean) {
  if (!store) return;
  try {
    const keys: string[] = [];
    for (let i = 0; i < store.length; i++) {
      const key = store.key(i);
      if (key && match(key)) keys.push(key);
    }
    for (const key of keys) store.removeItem(key);
  } catch {
    // Storage is optional.
  }
}

/**
 * Selects the queue of the confirmed user and deletes the stored queues of
 * every other user of this browser. A question typed in this tab before the
 * session named a user goes to that user. Null means signed out.
 */
export function setOfflineQueueUser(id: string | null): void {
  ensureInitialized();
  if (id === userId) return;
  const pending = userId || !id ? [] : readItems();
  ownSending.clear();
  storageBroken = false;
  if (!id) {
    userId = null;
    memoryQueue = [];
    refresh();
    return;
  }
  userId = id;
  signedOutHere = false;
  memoryQueue = [];
  const mine = `${USER_KEY_PREFIX}${id}`;
  removeKeys(webStorage("localStorage"), (key) => key.startsWith(USER_KEY_PREFIX) && key !== mine);
  const own = readItems();
  const ids = new Set(own.map((item) => item.id));
  writeItems([...own, ...pending.filter((item) => !ids.has(item.id))]);
  if (pending.length > 0) removeKeys(webStorage("sessionStorage"), (key) => key === PENDING_KEY);
}

/** Removes every stored queue and the remembered user. Sign-out calls it. */
export function clearOfflineQueues(): void {
  removeKeys(webStorage("localStorage"), (key) => key.startsWith(OFFLINE_QUEUE_STORAGE_PREFIX));
  removeKeys(webStorage("sessionStorage"), (key) => key.startsWith(OFFLINE_QUEUE_STORAGE_PREFIX));
  userId = null;
  signedOutHere = true;
  memoryQueue = [];
  storageBroken = false;
  ownSending.clear();
  refresh([]);
}

/**
 * Runs fn while no other tab of this browser sends from the queue or cancels
 * a question. With no Web Locks, fn runs at once; the server then matches a
 * question sent twice by its client request id.
 */
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

/**
 * A queued question completes on 2xx only. 408, 409, 425, 429 and 5xx send it
 * again later. A 401 or a 409 for another signed-in user keeps it for its
 * user to send after the next sign-in.
 */
export function offlineSendOutcome(
  status: number,
  code?: string,
): "done" | "retry" | "signin" | "refused" {
  if (status >= 200 && status < 300) return "done";
  if (status === 401 || (status === 409 && code === "user_mismatch")) return "signin";
  if (status === 408 || status === 409 || status === 425 || status === 429 || status >= 500) {
    return "retry";
  }
  return "refused";
}

/** The wait after a refusal: Retry-After or a default, doubled per earlier try. */
export function offlineRetryDelayMs(
  res: { status: number; headers: Headers },
  attempts = 0,
): number {
  const seconds = Number(res.headers.get("Retry-After"));
  const first =
    Number.isFinite(seconds) && seconds > 0
      ? Math.min(Math.max(seconds, 5), 300) * 1000
      : res.status === 429
        ? 30_000
        : 15_000;
  return Math.min(first * 2 ** Math.min(attempts, 10), OFFLINE_RETRY_MAX_MS);
}

/** The wait after a send that did not reach the server. */
export function offlineNetworkDelayMs(attempts = 0): number {
  return Math.min(1000 * 2 ** Math.min(attempts, 10), OFFLINE_NETWORK_RETRY_MAX_MS);
}

function attemptsOf(id: string): number {
  return readItems().find((item) => item.id === id)?.attempts ?? 0;
}

/**
 * Applies a non-2xx answer to a question in flight: it waits for a later
 * send, or it keeps the server's refusal and does not send again.
 */
export function settleOfflineRefusal(
  id: string,
  res: { status: number; headers: Headers; code?: string },
  error: string,
  threadId?: string,
): "retry" | "signin" | "refused" {
  const outcome = offlineSendOutcome(res.status, res.code);
  if (outcome === "done") throw new Error(`HTTP ${res.status} is not a refusal`);
  if (outcome === "refused") {
    failOfflineQuestion(id, `The server refused this question (HTTP ${res.status}): ${error}`, threadId);
  } else {
    deferOfflineQuestion(id, Date.now() + offlineRetryDelayMs(res, attemptsOf(id)), threadId);
  }
  return outcome;
}

/** Keeps the question to send again after a send that did not reach the server. */
export function deferAfterNetworkFailure(id: string, threadId?: string | null): void {
  deferOfflineQuestion(id, Date.now() + offlineNetworkDelayMs(attemptsOf(id)), threadId ?? undefined);
}

export function offlineQueueUser(): string | null {
  return userId;
}

export function getOfflineQueueSnapshot(): readonly QueuedQuestion[] {
  ensureInitialized();
  return queue;
}

export function isOfflineStorageBroken(): boolean {
  return storageBroken;
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

export function useOfflineStorageBroken(): boolean {
  return useSyncExternalStore(subscribeOfflineQueue, isOfflineStorageBroken, () => false);
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

/**
 * Removes a waiting or refused question. It waits for a send of another tab
 * to end, so a question that is being posted cannot be cancelled.
 */
export async function cancelOfflineQuestion(id: string): Promise<boolean> {
  ensureInitialized();
  if (ownSending.has(id)) return false;
  return withOfflineQueueLock(async () => {
    if (ownSending.has(id)) return false;
    const items = readItems();
    if (!items.some((item) => item.id === id)) {
      refresh(items);
      return false;
    }
    writeItems(items.filter((item) => item.id !== id));
    return true;
  });
}

function threadKey(item: QueuedQuestion): string {
  return `${item.threadBase} ${item.threadId ?? ""}`;
}

/**
 * Takes the first question of a thread to send, in the order typed. A thread
 * whose first question waits, failed or is in flight sends nothing else.
 * Call it inside withOfflineQueueLock.
 */
export function claimNextOfflineQuestion(
  accept: (item: QueuedQuestion) => boolean = () => true,
): QueuedQuestion | null {
  ensureInitialized();
  if (!userId) return null;
  const now = Date.now();
  const seen = new Set<string>();
  for (const item of readItems()) {
    const key = threadKey(item);
    if (seen.has(key)) continue;
    seen.add(key);
    if (
      item.status === "failed" ||
      ownSending.has(item.id) ||
      (item.retryAt && item.retryAt > now) ||
      !accept(item)
    ) {
      continue;
    }
    ownSending.add(item.id);
    refresh();
    return { ...item, status: "sending" };
  }
  return null;
}

/** Gives the thread that the first new-chat question created to the rest. */
export function moveNewChatQuestions(threadBase: "/work" | "/chat/t", threadId: string): void {
  ensureInitialized();
  const items = readItems();
  if (!items.some((item) => item.threadId === null && item.threadBase === threadBase)) return;
  writeItems(
    items.map((item) =>
      item.threadId === null && item.threadBase === threadBase ? { ...item, threadId } : item,
    ),
  );
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
  delete next.retryAt;
  delete next.error;
  return next;
}

/** Keeps the question to send again at retryAt, one try more. */
export function deferOfflineQuestion(id: string, retryAt: number, threadId?: string): void {
  updateOfflineQuestion(id, (item) => ({
    ...released(item),
    status: "waiting",
    attempts: (item.attempts ?? 0) + 1,
    retryAt: Math.min(retryAt, Date.now() + OFFLINE_RETRY_MAX_MS),
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

/** Sends a refused question again, at once. */
export function retryOfflineQuestion(id: string): void {
  updateOfflineQuestion(id, (item) => {
    const next: QueuedQuestion = { ...released(item), status: "waiting" };
    delete next.attempts;
    return next;
  });
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

/** Gives a question in flight back to the queue, if it still exists. */
export function requeueOfflineQuestion(item: QueuedQuestion): void {
  updateOfflineQuestion(item.id, (existing) => ({
    ...existing,
    status: existing.status === "failed" ? "failed" : "waiting",
    ...(item.threadId ? { threadId: item.threadId } : {}),
  }));
}

export function resetOfflineQueueForTests(): void {
  if (initialized && typeof window !== "undefined") {
    window.removeEventListener?.("storage", onStorage);
  }
  initialized = false;
  userId = null;
  signedOutHere = false;
  memoryQueue = [];
  storageBroken = false;
  ownSending.clear();
  queue = EMPTY_QUEUE;
  listeners.clear();
}

export function clearOfflineQueueForTests(): void {
  ownSending.clear();
  writeItems([]);
}
