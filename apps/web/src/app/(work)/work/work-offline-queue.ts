"use client";

import { useSyncExternalStore } from "react";

export const OFFLINE_QUEUE_STORAGE_KEY = "openneko.work.offlineQueue";

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
let initialized = false;
let queue: QueuedQuestion[] = [];
let nextCounter = 1;

function notify() {
  for (const listener of listeners) listener();
}

function readStoredQueue(): QueuedQuestion[] {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(OFFLINE_QUEUE_STORAGE_KEY);
    if (!raw) return [];
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
          status: "waiting",
        });
      }
    }
    return items;
  } catch {
    return [];
  }
}

function persistWaitingQueue() {
  if (typeof window === "undefined") return;
  try {
    const waiting = queue.filter((item) => item.status === "waiting");
    if (waiting.length === 0) {
      window.localStorage.removeItem(OFFLINE_QUEUE_STORAGE_KEY);
    } else {
      window.localStorage.setItem(OFFLINE_QUEUE_STORAGE_KEY, JSON.stringify(waiting));
    }
  } catch {
    // Storage may be unavailable in private browsing; in-memory queue still works.
  }
}

function ensureInitialized() {
  if (initialized || typeof window === "undefined") return;
  initialized = true;
  queue = readStoredQueue();
}

export function getOfflineQueueSnapshot(): readonly QueuedQuestion[] {
  ensureInitialized();
  return queue.length === 0 ? EMPTY_QUEUE : queue;
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
  const id = input.id ?? `offline-${Date.now()}-${nextCounter++}`;
  const existing = queue.find((item) => item.id === id);
  if (existing) return existing;
  const item: QueuedQuestion = {
    id,
    threadId: input.threadId,
    threadBase: input.threadBase,
    content: input.content,
    createdAt: input.createdAt ?? new Date().toISOString(),
    status: "waiting",
  };
  queue = [...queue, item];
  persistWaitingQueue();
  notify();
  return item;
}

export function cancelOfflineQuestion(id: string): boolean {
  ensureInitialized();
  const target = queue.find((item) => item.id === id && item.status === "waiting");
  if (!target) return false;
  queue = queue.filter((item) => item.id !== id);
  persistWaitingQueue();
  notify();
  return true;
}

export function claimNextOfflineQuestion(
  threadId: string | null,
  threadBase: "/work" | "/chat/t",
): QueuedQuestion | null {
  ensureInitialized();
  const index = queue.findIndex(
    (item) =>
      item.status === "waiting" &&
      item.threadBase === threadBase &&
      item.threadId === threadId,
  );
  if (index === -1) return null;
  const claimed: QueuedQuestion = {
    ...queue[index],
    status: "sending",
  };
  queue = [
    ...queue.slice(0, index),
    claimed,
    ...queue.slice(index + 1),
  ];
  persistWaitingQueue();
  notify();
  return claimed;
}

export function completeOfflineQuestion(id: string): void {
  ensureInitialized();
  if (!queue.some((item) => item.id === id)) return;
  queue = queue.filter((item) => item.id !== id);
  persistWaitingQueue();
  notify();
}

export function requeueOfflineQuestion(item: QueuedQuestion): void {
  ensureInitialized();
  const waitingItem: QueuedQuestion = { ...item, status: "waiting" };
  const index = queue.findIndex((existing) => existing.id === item.id);
  if (index >= 0) {
    queue = [
      ...queue.slice(0, index),
      waitingItem,
      ...queue.slice(index + 1),
    ];
  } else {
    queue = [waitingItem, ...queue];
  }
  persistWaitingQueue();
  notify();
}

export function clearOfflineQueueForTests(): void {
  initialized = true;
  queue = [];
  persistWaitingQueue();
  notify();
}
