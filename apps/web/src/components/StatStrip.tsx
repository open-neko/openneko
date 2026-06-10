"use client";

// OL9 — the Briefing stat strip: four mono numbers, no chart.
// `12 runs today · 4 findings · 1 pending approval · 38% daily budget used`
// The budget number only surfaces at >= 40% (a heads-up before it bites).

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";

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
    void fetchStats();
    const id = setInterval(() => void fetchStats(), 30_000);
    return () => clearInterval(id);
  }, [fetchStats]);

  if (!stats) return null;

  const item = (
    label: string,
    n: number | string,
    onClick?: () => void,
  ) => (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      className="bg-transparent border-0 p-0 font-[inherit] text-[13px] text-text2 disabled:cursor-default cursor-pointer enabled:hover:text-text"
    >
      <span className="font-mono font-semibold text-text">{n}</span> {label}
    </button>
  );

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
        () => router.push("/approvals"),
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
