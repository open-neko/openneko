"use client";

import { useEffect, useState } from "react";

export type ClientSession = {
  user: { id: string; email: string; name: string | null } | null;
  role: "admin" | "member" | null;
  authEnabled: boolean;
  signedIn: boolean;
};

let pending: Promise<ClientSession | null> | null = null;
const watchers = new Set<(session: ClientSession | null) => void>();

export function forgetSession(): void {
  pending = null;
}

export function loadSession(): Promise<ClientSession | null> {
  pending ??= fetch("/api/v1/auth/session", { cache: "no-store" })
    .then((res) => (res.ok ? (res.json() as Promise<ClientSession>) : null))
    .catch(() => null)
    .then((session) => {
      if (!session) pending = null;
      return session;
    });
  return pending;
}

/**
 * Asks the server for the session now, past the cache, and tells every
 * watcher. Resolves to the user id, null when no user is signed in, or
 * undefined when the server could not be reached.
 */
export async function confirmSessionUser(): Promise<string | null | undefined> {
  forgetSession();
  let res: Response;
  try {
    res = await fetch("/api/v1/auth/session", { cache: "no-store" });
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
 * Loads the session now and again each time the browser comes back online,
 * so a page that loaded offline learns its user once the network returns.
 */
export function watchSession(onSession: (session: ClientSession | null) => void): () => void {
  let cancelled = false;
  const notify = (value: ClientSession | null) => {
    if (!cancelled) onSession(value);
  };
  const load = () => {
    void loadSession().then(notify);
  };
  const reload = () => {
    forgetSession();
    load();
  };
  watchers.add(notify);
  load();
  window.addEventListener("online", reload);
  return () => {
    cancelled = true;
    watchers.delete(notify);
    window.removeEventListener("online", reload);
  };
}

export function useSession(): ClientSession | null | undefined {
  const [session, setSession] = useState<ClientSession | null | undefined>(undefined);
  useEffect(() => watchSession(setSession), []);
  return session;
}
