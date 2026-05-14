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
    | "approvals"
    | "settings"
    | "business-profile";
  children?: React.ReactNode;
}) {
  const [pendingApprovals, setPendingApprovals] = useState<number>(0);

  // Poll the approvals count so the nav reveals/hides automatically.
  // Always check on mount; refresh every 30s while the tab is open.
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

  // Approvals link is always rendered when current === "approvals" (so the
  // user can navigate back away from it), even if the queue has emptied
  // since they landed.
  const showApprovals = pendingApprovals > 0 || current === "approvals";

  return (
    <div className="section-nav-row">
      <Link
        href="/"
        className={`settings-link nav-link${current === "dashboard" ? " is-active" : ""}`}
      >
        Dashboard
      </Link>
      <Link
        href="/work"
        className={`settings-link nav-link${current === "work" ? " is-active" : ""}`}
      >
        Ask
      </Link>
      <Link
        href="/workflows"
        className={`settings-link nav-link${current === "workflows" ? " is-active" : ""}`}
      >
        Workflows
      </Link>
      {showApprovals && (
        <Link
          href="/approvals"
          className={`settings-link nav-link${current === "approvals" ? " is-active" : ""}`}
        >
          Approvals
          {pendingApprovals > 0 && (
            <span className="nav-link-badge">{pendingApprovals}</span>
          )}
        </Link>
      )}
      <Link
        href="/business-profile"
        className={`settings-link nav-link${current === "business-profile" ? " is-active" : ""}`}
      >
        Business Profile
      </Link>
      <Link
        href="/settings"
        className={`settings-link nav-link${current === "settings" ? " is-active" : ""}`}
      >
        Settings
      </Link>
      {children}
    </div>
  );
}
