"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

export default function SectionNav({
  current,
  children,
}: {
  current:
    | "dashboard"
    | "workflows"
    | "work"
    | "actions"
    | "settings"
    | "business-profile";
  children?: React.ReactNode;
}) {
  const [pendingApprovals, setPendingApprovals] = useState<number>(0);

  // Poll the pending count so the badge stays current. The Actions link
  // itself is always visible — operators should always know they can find
  // the queue here, even when nothing is pending.
  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      try {
        const res = await fetch("/api/approvals?countOnly=true", {
          cache: "no-store",
        });
        if (cancelled) return;
        if (!res.ok) return;
        const data = (await res.json()) as { count?: number };
        setPendingApprovals(data.count ?? 0);
      } catch {
        // best-effort; ignore network blips
      }
    };
    void tick();
    const id = setInterval(tick, 30_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return (
    <nav className="topbar-nav">
      <Link
        href="/"
        className={`topbar-nav-link${current === "dashboard" ? " is-active" : ""}`}
      >
        Dashboard
      </Link>
      <Link
        href="/work"
        className={`topbar-nav-link${current === "work" ? " is-active" : ""}`}
      >
        Ask
      </Link>
      <Link
        href="/actions"
        className={`topbar-nav-link${current === "actions" ? " is-active" : ""}`}
      >
        Actions
        {pendingApprovals > 0 && (
          <span className="font-mono nav-link-badge">{pendingApprovals}</span>
        )}
      </Link>
      <Link
        href="/business-profile"
        className={`topbar-nav-link${current === "business-profile" ? " is-active" : ""}`}
      >
        Business Profile
      </Link>
      <Link
        href="/settings"
        className={`topbar-nav-link${current === "settings" ? " is-active" : ""}`}
      >
        Settings
      </Link>
      {children}
    </nav>
  );
}
