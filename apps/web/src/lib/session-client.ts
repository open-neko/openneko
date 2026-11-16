"use client";

import { useEffect, useState } from "react";

export type ClientSession = {
  user: { id: string; email: string; name: string | null } | null;
  role: "admin" | "member" | null;
  authEnabled: boolean;
  signedIn: boolean;
};

let pending: Promise<ClientSession | null | undefined> | null = null;
const watchers = new Set<(session: ClientSession | null) => void>();
export const SESSION_RETRY_MAX_MS = 30_000;

export function forgetSession(): void {
  pending = null;
}

/**
 * Resolves to the session, to null when no user is signed in, or to
 * undefined when the server could not answer.
 */
export function loadSession(): Promise<ClientSession | null | undefined> {
  pending ??= fetch("/api/v1/auth/session", { cache: "no-store" })
    .then(async (res): Promise<ClientSession | null | undefined> => {
      if (res.status === 401) return null;
      if (!res.ok) return undefined;
      return ((await res.json().catch(() => undefined)) as ClientSession | undefined) ?? undefined;
    })
    .catch(() => undefined)
    .then((session) => {
      if (session === undefined) pending = null;
      return session;
    });
  return pending;
}

/**
 * Asks the server for the session now, past the cache, and tells every
 * watcher. Resolves to the user id, null when no user is signed in, or
 * undefined when the server could not be reached before the signal aborts.
 */
export async function confirmSessionUser(signal?: AbortSignal): Promise<string | null | undefined> {
  forgetSession();
  let res: Response;
  try {
    res = await fetch("/api/v1/auth/session", { cache: "no-store", ...(signal ? { signal } : {}) });
  } catch {
    return undefined;
  }
  if (res.status === 401) {
    for (const watcher of watchers) watcher(null);
    return null;
  }
  if (!res.ok) return undefined;
  const session = (await res.json().catch(() => null)) as ClientSession | null;
  if (!session) return undefined;
  pending = Promise.resolve(session);
  for (const watcher of watchers) watcher(session);
  return session.user?.id ?? null;
}

/**
 * Loads the session now and again each time the browser comes back online.
 * While the server cannot answer, it asks again after a growing delay and
 * tells the watcher nothing, so "unknown" never reads as "signed out".
 */
export function watchSession(onSession: (session: ClientSession | null) => void): () => void {
  let cancelled = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let delay = 1000;
  const notify = (value: ClientSession | null) => {
    if (!cancelled) onSession(value);
  };
  const load = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    void loadSession().then((session) => {
      if (cancelled) return;
      if (session === undefined) {
        if (timer === null) {
          timer = setTimeout(load, delay);
          delay = Math.min(delay * 2, SESSION_RETRY_MAX_MS);
        }
        return;
      }
      delay = 1000;
      notify(session);
    });
  };
  const reload = () => {
    forgetSession();
    delay = 1000;
    load();
  };
  watchers.add(notify);
  load();
  window.addEventListener("online", reload);
  return () => {
    cancelled = true;
    if (timer !== null) clearTimeout(timer);
    watchers.delete(notify);
    window.removeEventListener("online", reload);
  };
}

/** The session; undefined while it is not known yet. */
export function useSession(): ClientSession | null | undefined {
  const [session, setSession] = useState<ClientSession | null | undefined>(undefined);
  useEffect(() => watchSession(setSession), []);
  return session;
}
