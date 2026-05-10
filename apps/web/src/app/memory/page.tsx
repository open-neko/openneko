"use client";

import { useCallback, useEffect, useState } from "react";
import { Brain, Pin, Trash2 } from "lucide-react";
import AppHeader from "@/components/AppHeader";
import { confirmDialog } from "@/components/ConfirmModal";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import WorkSidebar from "@/app/work/WorkSidebar";

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
      <div className="root">
        <AppHeader>
          <SectionNav current="work" />
        </AppHeader>

        <div className="work-layout">
          <WorkSidebar />
          <section className="work-panel">
            <div className="library-head">
          <div className="library-head-icon">
            <Brain size={16} strokeWidth={2} />
          </div>
          <div>
            <div className="library-title">Memory</div>
            <div className="library-sub">
              {loading ? "Loading…" : `${active.length} active · ${pending.length} pending`}
            </div>
          </div>
        </div>

        {pending.length > 0 ? (
          <section className="library-section">
            <div className="library-section-title">Pending review · {pending.length}</div>
            <div className="library-pending-list">
              {pending.map((item) => (
                <div key={item.id} className="library-pending">
                  <div className="library-pending-head">
                    <span className="library-pending-kind">{item.draftKind.replace(/_/g, " ")}</span>
                    <span className="library-pending-meta">~{Math.round(item.confidence * 100)}%</span>
                  </div>
                  <div className="library-pending-text">&ldquo;{item.draftText}&rdquo;</div>
                  {item.reasoning ? (
                    <div className="library-pending-reason">{item.reasoning}</div>
                  ) : null}
                  <div className="library-pending-actions">
                    <button
                      type="button"
                      disabled={busyId === item.id}
                      onClick={() => void decide(item.id, "accept", { scope: "global" })}
                      className="library-btn is-primary"
                    >
                      Save globally
                    </button>
                    <button
                      type="button"
                      disabled={busyId === item.id}
                      onClick={() => void decide(item.id, "decline")}
                      className="library-btn"
                    >
                      Decline
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>
        ) : null}

        <section className="library-section">
          <div className="library-section-title">Saved memories · {active.length}</div>
          {active.length === 0 ? (
            <div className="library-empty is-compact">
              Nothing saved yet. Memories appear here when you tell the agent to remember something,
              or when the auto-classifier promotes a turn into a stable rule.
            </div>
          ) : (
            <ul className="library-list">
              {active.map((memory) => (
                <li key={memory.id} className="library-item is-static">
                  <div className="library-item-main">
                    <div className="library-item-meta">
                      <span className="library-meta-pill">{memory.kind.replace(/_/g, " ")}</span>
                      <span>
                        {memory.scope}
                        {memory.scopeId ? `:${memory.scopeId.slice(0, 8)}` : ""}
                      </span>
                      {memory.pinned ? (
                        <span className="library-meta-pill">
                          <Pin size={11} strokeWidth={2} /> pinned
                        </span>
                      ) : null}
                      {memory.useCount && memory.useCount > 0 ? (
                        <span>used {memory.useCount}×</span>
                      ) : null}
                    </div>
                    <div className="library-item-body">{memory.text}</div>
                    {memory.createdAt ? (
                      <div className="library-item-meta">
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
                    className="library-icon-btn library-row-action"
                  >
                    <Trash2 size={14} strokeWidth={2} />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
          </section>
        </div>
      </div>
      <CreatorCredit />
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
