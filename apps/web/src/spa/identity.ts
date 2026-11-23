import type { RailIdentity } from "@/components/AppRail";
import { reportNetworkOffline, reportNetworkOnline } from "./pwa";

type SessionResponse = {
  user: { email: string; name: string | null } | null;
  role: "admin" | "member" | null;
  authEnabled?: boolean;
  signedIn?: boolean;
};

export function identityFromSession(data: SessionResponse): RailIdentity | undefined {
  if (!data.user) return undefined;
  return {
    user: { email: data.user.email, name: data.user.name },
    mode: data.authEnabled === false ? "solo" : data.role === "member" ? "member" : "admin",
    signedIn: Boolean(data.signedIn),
  };
}

export async function loadIdentity(): Promise<RailIdentity | undefined> {
  try {
    const res = await fetch("/api/v1/auth/session", { cache: "no-store" });
    reportNetworkOnline();
    return res.ok ? identityFromSession((await res.json()) as SessionResponse) : undefined;
  } catch {
    reportNetworkOffline();
    return undefined;
  }
}
