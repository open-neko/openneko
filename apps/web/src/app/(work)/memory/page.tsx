"use client";

import { useCallback, useEffect, useState } from "react";
import { Brain, Pin, Trash2 } from "lucide-react";
import { confirmDialog } from "@/components/ConfirmModal";

type MemoryRow = {
  id: string;
  kind: string;
  scope: string;
  scopeId: string | null;
  text: string;
  pinned: boolean;
  confidence: number;
  useCount?: number;
  lastUsedAt?: string | null;
  archivedAt?: string | null;
  createdAt?: string;
  updatedAt?: string;
};

type PendingRow = {
  id: string;
  draftText: string;
  draftKind: string;
  draftScope: string;
  confidence: number;
  reasoning: string | null;
};

export default function MemoryPage() {
  const [memories, setMemories] = useState<MemoryRow[]>([]);
  const [pending, setPending] = useState<PendingRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const [mRes, pRes] = await Promise.all([
      fetch("/api/work/memories", { cache: "no-store" }).then((r) => r.json()),
      fetch("/api/work/memories/pending", { cache: "no-store" }).then((r) => r.json()),
    ]);
    setMemories((mRes.memories as MemoryRow[]) ?? []);
    setPending((pRes.pending as PendingRow[]) ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const archive = useCallback(
    async (id: string) => {
      const ok = await confirmDialog({
        title: "Archive this memory?",
        description: "It stops being injected into future runs.",
        confirmLabel: "Archive",
        destructive: true,
      });
      if (!ok) return;
      setBusyId(id);
      try {
        const res = await fetch(`/api/work/memories/${encodeURIComponent(id)}`, {
          method: "DELETE",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ reason: "manual archive from /memory" }),
        });
        if (res.ok) await refresh();
      } finally {
        setBusyId(null);
      }
    },
    [refresh],
  );

  const decide = useCallback(
    async (id: string, action: "accept" | "decline", overrides?: { scope?: string; scopeId?: string | null }) => {
      setBusyId(id);
      try {
        const res = await fetch("/api/work/memories/decide", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ id, action, ...overrides }),
        });
        if (res.ok) await refresh();
      } finally {
        setBusyId(null);
      }
    },
    [refresh],
  );

  const active = memories.filter((memory) => !memory.archivedAt);

  return (
    <>
      <div className="flex items-center gap-3 mb-6">
        <div className="w-9 h-9 rounded-xl bg-accent-soft text-accent inline-flex items-center justify-center shrink-0">
          <Brain size={16} strokeWidth={2} />
        </div>
        <div>
          <div className="font-display text-2xl font-bold leading-[1.1] text-text">Memory</div>
          <div className="text-[13px] text-text3 mt-0.5">
            {loading ? "Loading…" : `${active.length} active · ${pending.length} pending`}
          </div>
        </div>
      </div>

      {pending.length > 0 ? (
        <section className="mt-7">
          <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-text3 mb-2.5">Pending review · {pending.length}</div>
          <div className="flex flex-col gap-2.5">
            {pending.map((item) => (
              <div key={item.id} className="border border-[#f4d27a] bg-[#fff7e0] rounded-2xl px-4 py-3.5">
                <div className="flex items-center justify-between gap-3 mb-1.5">
                  <span className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[#8b6512]">{item.draftKind.replace(/_/g, " ")}</span>
                  <span className="text-[11px] text-[#b18128] tabular-nums">~{Math.round(item.confidence * 100)}%</span>
                </div>
                <div className="text-[13.5px] leading-[1.5] text-[#4a3a16] italic">&ldquo;{item.draftText}&rdquo;</div>
                {item.reasoning ? (
                  <div className="mt-1.5 text-xs text-[#8b6512]">{item.reasoning}</div>
                ) : null}
                <div className="mt-2.5 flex gap-1.5 flex-wrap">
                  <button
                    type="button"
                    disabled={busyId === item.id}
                    onClick={() => void decide(item.id, "accept", { scope: "global" })}
                    className="text-xs px-[11px] py-[5px] rounded-lg border border-[#8b6512] bg-[#8b6512] text-white cursor-pointer transition hover:enabled:bg-[#6b4d10] disabled:opacity-60 disabled:cursor-not-allowed"
                  >
                    Save globally
                  </button>
                  <button
                    type="button"
                    disabled={busyId === item.id}
                    onClick={() => void decide(item.id, "decline")}
                    className="text-xs px-[11px] py-[5px] rounded-lg border border-[#e5b95a] bg-white text-[#6b4d10] cursor-pointer transition hover:enabled:bg-[#fff2cc] disabled:opacity-60 disabled:cursor-not-allowed"
                  >
                    Decline
                  </button>
                </div>
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <section className="mt-7">
        <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-text3 mb-2.5">Saved memories · {active.length}</div>
        {active.length === 0 ? (
          <div className="bg-card border border-dashed border-border rounded-2xl px-4 py-3.5 text-[13px] leading-[1.55] text-text3">
            Nothing saved yet. Memories appear here when you tell the agent to remember something,
            or when the auto-classifier promotes a turn into a stable rule.
          </div>
        ) : (
          <ul className="list-none p-0 m-0 flex flex-col gap-2">
            {active.map((memory) => (
              <li key={memory.id} className="group relative flex items-start gap-2 p-1 bg-card border border-border rounded-2xl text-inherit list-none">
                <div className="flex-1 min-w-0 flex flex-col gap-1 px-3 py-2.5 rounded-[10px]">
                  <div className="flex flex-wrap items-center gap-2.5 text-[11.5px] text-text3">
                    <span className="inline-flex items-center gap-1 bg-black/5 text-text2 px-2 py-0.5 rounded-full text-[11px] font-medium">{memory.kind.replace(/_/g, " ")}</span>
                    <span>
                      {memory.scope}
                      {memory.scopeId ? `:${memory.scopeId.slice(0, 8)}` : ""}
                    </span>
                    {memory.pinned ? (
                      <span className="inline-flex items-center gap-1 bg-black/5 text-text2 px-2 py-0.5 rounded-full text-[11px] font-medium">
                        <Pin size={11} strokeWidth={2} /> pinned
                      </span>
                    ) : null}
                    {memory.useCount && memory.useCount > 0 ? (
                      <span>used {memory.useCount}×</span>
                    ) : null}
                  </div>
                  <div className="text-[13.5px] leading-[1.5] text-text my-0.5">{memory.text}</div>
                  {memory.createdAt ? (
                    <div className="flex flex-wrap items-center gap-2.5 text-[11.5px] text-text3">
                      <span>created {formatDate(memory.createdAt)}</span>
                      {memory.lastUsedAt ? (
                        <span>last used {formatDate(memory.lastUsedAt)}</span>
                      ) : null}
                    </div>
                  ) : null}
                </div>
                <button
                  type="button"
                  disabled={busyId === memory.id}
                  onClick={() => void archive(memory.id)}
                  aria-label="Archive memory"
                  title="Archive memory"
                  className="mt-1.5 mr-1.5 w-9 h-9 rounded-[9px] bg-transparent border-0 text-text3 inline-flex items-center justify-center transition opacity-0 pointer-events-none cursor-pointer hover:bg-[rgba(220,53,69,0.1)] hover:text-[var(--danger-hover)] disabled:opacity-50 disabled:cursor-not-allowed group-hover:opacity-100 group-hover:pointer-events-auto group-focus-within:opacity-100 group-focus-within:pointer-events-auto [@media(hover:none)]:opacity-100 [@media(hover:none)]:pointer-events-auto"
                >
                  <Trash2 size={14} strokeWidth={2} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

function formatDate(value: string): string {
  return new Date(value).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}
