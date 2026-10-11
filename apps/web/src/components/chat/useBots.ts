"use client";

import { useEffect, useState } from "react";
import type { BotSummary } from "@/lib/bots";

export function useBots() {
  const [bots, setBots] = useState<BotSummary[] | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/bots")
      .then((res) => (res.ok ? res.json() : Promise.reject(res)))
      .then((data: { bots?: BotSummary[] }) => {
        if (!cancelled) setBots(data.bots ?? []);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { bots, failed };
}
