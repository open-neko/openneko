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
      <div className="w-full max-w-[1000px] px-5 flex items-start justify-between gap-4 min-h-[41px]">
        <div className="flex items-center gap-2.5 flex-1 min-w-0 flex-wrap">
          {back && (
            <Link className="settings-backlink" href={back.href}>
              <ArrowLeft size={14} strokeWidth={2.25} aria-hidden="true" className="settings-backlink-arrow" />
              <span>{back.label}</span>
            </Link>
          )}
          {children}
        </div>

        <div className="inline-flex items-center gap-2.5 self-start">
          <a
            href={MARKETING_URL}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-2 select-none text-text no-underline transition-opacity duration-200 hover:opacity-80 self-start h-[41px]"
            aria-label="OpenNeko — open marketing site in a new tab"
          >
            <img className="w-6 h-6 object-contain block flex-none" src="/cat.png" alt="" width={24} height={24} />
            <span className="font-display text-[17px] font-extrabold tracking-[-0.04em] leading-none">
              OpenNeko
            </span>
            <span aria-hidden="true" className="text-text3 text-sm leading-none mx-0.5">·</span>
            <span aria-hidden="true" className="font-mono text-[10.5px] font-semibold text-text3 tracking-wider lowercase bg-neutral px-1.5 py-0.5 rounded-full leading-none">
              v{APP_VERSION}
            </span>
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
