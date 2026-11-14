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

export function useSession(): ClientSession | null | undefined {
  const [session, setSession] = useState<ClientSession | null | undefined>(undefined);
  useEffect(() => {
    let cancelled = false;
    void loadSession().then((value) => {
      if (!cancelled) setSession(value);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return session;
}
