"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ArrowLeft } from "lucide-react";
import DensityToggle from "@/components/DensityToggle";

const MARKETING_URL = "https://getneko.app";
const APP_VERSION = process.env.NEXT_PUBLIC_APP_VERSION ?? "0.0.0";
const VERSION_POLL_MS = 60_000;

export type AppHeaderProps = {
  back?: { href: string; label: string };
  children?: React.ReactNode;
};

// Single top bar matching the dense mockups: brand + version pill (left),
// section nav, then the density toggle (right). The "update available" state
// folds into the version pill so the bar stays mockup-faithful.
export default function AppHeader({ back, children }: AppHeaderProps) {
  const [latestVersion, setLatestVersion] = useState<string | null>(null);
  const [user, setUser] = useState<{ email: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    const check = async () => {
      try {
        const res = await fetch("/api/version", { cache: "no-store" });
        if (!res.ok) return;
        const data = (await res.json()) as { version?: string };
        if (cancelled) return;
        if (typeof data.version === "string") setLatestVersion(data.version);
      } catch {
        // best-effort
      }
    };
    void check();
    const id = setInterval(check, VERSION_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/auth/session", { cache: "no-store" });
        if (cancelled || !res.ok) return;
        const data = (await res.json()) as {
          user: { id: string; email: string; name: string | null } | null;
        };
        if (data.user) setUser({ email: data.user.email });
      } catch {
        // best-effort
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  async function handleSignOut() {
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } finally {
      window.location.href = "/signin";
    }
  }

  const updateAvailable =
    latestVersion !== null && latestVersion !== APP_VERSION;

  return (
    <header className="app-header">
      <div className="topbar-inner">
        <a
          className="topbar-brand"
          href={MARKETING_URL}
          target="_blank"
          rel="noreferrer"
          aria-label="OpenNeko — open marketing site in a new tab"
        >
          <img className="topbar-logo" src="/cat.png" alt="" width={22} height={22} />
          <span className="topbar-name">OpenNeko</span>
          {updateAvailable ? (
            <button
              type="button"
              className="topbar-ver is-update"
              onClick={() => window.location.reload()}
              aria-label={`Update available: v${latestVersion}. Reload to apply.`}
              title={`v${latestVersion} available — reload`}
            >
              <span className="topbar-ver-dot" aria-hidden="true" />
              v{latestVersion} · reload
            </button>
          ) : (
            <span className="topbar-ver">{APP_VERSION}</span>
          )}
        </a>

        {back && (
          <Link className="settings-backlink" href={back.href}>
            <ArrowLeft size={14} strokeWidth={2.25} aria-hidden="true" className="settings-backlink-arrow" />
            <span>{back.label}</span>
          </Link>
        )}

        {children}

        <span className="topbar-spacer" />

        <DensityToggle />

        {user && (
          <button
            type="button"
            onClick={handleSignOut}
            aria-label={`Sign out ${user.email}`}
            title={user.email}
            className="topbar-signout"
          >
            Sign out
          </button>
        )}
      </div>
    </header>
  );
}
