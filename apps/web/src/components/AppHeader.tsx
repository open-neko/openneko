"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ArrowLeft } from "lucide-react";

const MARKETING_URL = "https://getneko.app";
const APP_VERSION = process.env.NEXT_PUBLIC_APP_VERSION ?? "0.0.0";
const VERSION_POLL_MS = 60_000;

export type AppHeaderProps = {
  back?: { href: string; label: string };
  children?: React.ReactNode;
};

export default function AppHeader({ back, children }: AppHeaderProps) {
  const [latestVersion, setLatestVersion] = useState<string | null>(null);

  // Quiet poll for the server's current version. When it diverges from the
  // version baked into this client bundle at build time, a new deploy has
  // landed — the brand chip turns into a reload button.
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

  const updateAvailable =
    latestVersion !== null && latestVersion !== APP_VERSION;

  return (
    <header className="app-header">
      <div className="app-header-inner">
        <div className="app-header-left">
          {back && (
            <Link className="settings-backlink" href={back.href}>
              <ArrowLeft size={14} strokeWidth={2.25} aria-hidden="true" className="settings-backlink-arrow" />
              <span>{back.label}</span>
            </Link>
          )}
          {children}
        </div>

        <div className="brand-cluster">
          <a
            href={MARKETING_URL}
            target="_blank"
            rel="noreferrer"
            className="brand"
            style={{ textDecoration: "none" }}
            aria-label="OpenNeko — open marketing site in a new tab"
          >
            <img className="brand-icon" src="/cat.png" alt="" width={24} height={24} />
            <span className="brand-name">OpenNeko</span>
            <span aria-hidden="true" className="brand-tick">·</span>
            <span aria-hidden="true" className="brand-version">v{APP_VERSION}</span>
          </a>

          {updateAvailable && (
            <button
              type="button"
              className="brand-update"
              onClick={() => window.location.reload()}
              aria-label={`Update available: v${latestVersion}. Reload to apply.`}
              title={`v${latestVersion} available — reload`}
            >
              <span className="brand-update-dot" aria-hidden="true" />
              <span className="brand-update-label">v{latestVersion} ready · reload</span>
            </button>
          )}
        </div>
      </div>
    </header>
  );
}
