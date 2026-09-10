"use client";

// OL9 — the Briefing stat strip: four mono numbers, no chart.
// `12 runs today · 4 findings · 1 pending approval · 38% daily budget used`
// The budget number only surfaces at >= 40% (a heads-up before it bites).

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";

type Stats = {
  runsToday: number;
  findingsToday: number;
  pendingApprovals: number;
  budgetPct: number | null;
};

export default function StatStrip() {
  const router = useRouter();
  const [stats, setStats] = useState<Stats | null>(null);

  const fetchStats = useCallback(async () => {
    try {
      const res = await fetch("/api/briefing/stats", { cache: "no-store" });
      if (!res.ok) return;
      setStats((await res.json()) as Stats);
    } catch {
      // best-effort; the strip just stays hidden
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => void fetchStats(), 0);
    const id = window.setInterval(() => void fetchStats(), 30_000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(id);
    };
  }, [fetchStats]);

  if (!stats) return null;

  const item = (label: string, n: number | string, onClick?: () => void) => {
    const content = (
      <>
        <span className="font-mono font-semibold text-text">{n}</span> {label}
      </>
    );

    if (!onClick) {
      return <span className="text-ui-body-sm text-text2">{content}</span>;
    }

    return (
      <Button
        variant="ghost"
        type="button"
        onClick={onClick}
        data-ui-stat-link
        className="min-h-8 bg-transparent border-0 p-0 font-[inherit] text-ui-body-sm text-text2 cursor-pointer hover:text-text"
      >
        {content}
      </Button>
    );
  };

  return (
    <div className="flex items-center gap-2 flex-wrap text-text3 mb-4">
      {item(
        stats.runsToday === 1 ? "run today" : "runs today",
        stats.runsToday,
        () => router.push("/runs"),
      )}
      <span className="opacity-50">·</span>
      {item(
        stats.findingsToday === 1 ? "finding" : "findings",
        stats.findingsToday,
      )}
      <span className="opacity-50">·</span>
      {item(
        stats.pendingApprovals === 1 ? "pending approval" : "pending approvals",
        stats.pendingApprovals,
        () => router.push("/actions"),
      )}
      {stats.budgetPct !== null && stats.budgetPct >= 40 && (
        <>
          <span className="opacity-50">·</span>
          {item("daily budget used", `${stats.budgetPct}%`)}
        </>
      )}
    </div>
  );
}
