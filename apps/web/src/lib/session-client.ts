"use client";

import { useEffect, useState } from "react";

export type ClientSession = {
  user: { id: string; email: string; name: string | null } | null;
  role: "admin" | "member" | null;
  authEnabled: boolean;
  signedIn: boolean;
};

let pending: Promise<ClientSession | null> | null = null;

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
 * Loads the session now and again each time the browser comes back online,
 * so a page that loaded offline learns its user once the network returns.
 */
export function watchSession(onSession: (session: ClientSession | null) => void): () => void {
  let cancelled = false;
  const load = () => {
    void loadSession().then((value) => {
      if (!cancelled) onSession(value);
    });
  };
  load();
  window.addEventListener("online", load);
  return () => {
    cancelled = true;
    window.removeEventListener("online", load);
  };
}

export function useSession(): ClientSession | null | undefined {
  const [session, setSession] = useState<ClientSession | null | undefined>(undefined);
  useEffect(() => watchSession(setSession), []);
  return session;
}
