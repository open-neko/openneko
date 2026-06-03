"use client";

import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import ActCard, {
  type ActCardData,
  type ActRowData,
  type ActRowTone,
} from "@/components/ActCard";
import { cn } from "@/lib/cn";

type Filter = "awaiting" | "fired" | "rejected" | "all";

type ActionRow = {
  id: string;
  workflowRunId: string;
  workflow: { id: string; name: string };
  triggeredByObservation: { title: string } | null;
  kind: string;
  target: string | null;
  payload: unknown;
  riskLevel: string | null;
  summary: string;
  scope: string;
  status: string;
  minutesSaved: number | null;
  approvedAt: string | null;
  approverKind: "operator" | "policy" | "auto" | null;
  approverLabel: string | null;
  rejectionReason: string | null;
  runAt: string;
  createdAt: string;
};

type ActionsPayload = {
  actions: ActionRow[];
  count: number;
  filter: Filter;
};

const TABS: Array<{ key: Filter; label: string }> = [
  { key: "fired", label: "Fired" },
  { key: "awaiting", label: "Awaiting you" },
  { key: "rejected", label: "Rejected" },
  { key: "all", label: "All" },
];

function approverPhrase(
  kind: ActionRow["approverKind"],
  label: string | null,
): string | null {
  if (kind === "operator") return label ? `you · ${label}` : "you";
  if (kind === "policy") return label ? `rule "${label}"` : "a rule";
  if (kind === "auto") return "auto";
  return null;
}

function rowToneFor(row: ActionRow): ActRowTone {
  if (row.status === "rejected" || row.status === "failed") return "action";
  if (row.status === "pending_approval") {
    if (row.riskLevel === "high" || row.riskLevel === "critical") return "action";
    return "watch";
  }
  return "good";
}

function stateFor(row: ActionRow): ActCardData["state"] {
  if (row.status === "pending_approval") return "awaiting";
  if (row.status === "rejected" || row.status === "failed") return "rejected";
  return "live";
}

function isFilter(value: string | null): value is Filter {
  return (
    value === "awaiting" ||
    value === "fired" ||
    value === "rejected" ||
    value === "all"
  );
}

type Group = {
  key: string;
  runId: string;
  runAt: string;
  trigger: string | null;
  workflowName: string;
  state: ActCardData["state"];
  rows: ActionRow[];
};

function groupActions(actions: ActionRow[]): Group[] {
  // Key groups by (runId, state) so a mixed-status run produces separate cards
  // with consistent badges. Within a group, rows stay time-ordered (API order).
  const map = new Map<string, Group>();
  for (const row of actions) {
    const state = stateFor(row);
    const key = `${row.workflowRunId}:${state}`;
    const existing = map.get(key);
    if (existing) {
      existing.rows.push(row);
    } else {
      map.set(key, {
        key,
        runId: row.workflowRunId,
        runAt: row.runAt,
        trigger: row.triggeredByObservation?.title ?? null,
        workflowName: row.workflow.name,
        state,
        rows: [row],
      });
    }
  }
  return Array.from(map.values());
}

export default function ActionsPage() {
  return (
    <Suspense fallback={null}>
      <ActionsPageInner />
    </Suspense>
  );
}

function ActionsPageInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const initial = searchParams?.get("filter");
  const [filter, setFilter] = useState<Filter>(isFilter(initial) ? initial : "awaiting");
  const [data, setData] = useState<ActionsPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [focusedId, setFocusedId] = useState<string | null>(null);
  const [rejectingId, setRejectingId] = useState<string | null>(null);
  const [rejectReason, setRejectReason] = useState("");
  const rowRefs = useRef<Record<string, HTMLLIElement | null>>({});

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/approvals?filter=${filter}`, {
        cache: "no-store",
      });
      if (!res.ok) {
        setError(`Couldn't load (HTTP ${res.status})`);
        return;
      }
      const json = (await res.json()) as ActionsPayload;
      setData(json);
      if (filter === "awaiting" && !focusedId && json.actions[0]) {
        setFocusedId(json.actions[0].id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load");
    }
  }, [filter, focusedId]);

  useEffect(() => {
    void load();
  }, [load]);

  const switchFilter = useCallback((next: Filter) => {
    setFilter(next);
    setFocusedId(null);
    setRejectingId(null);
    const url = new URL(window.location.href);
    if (next === "awaiting") url.searchParams.delete("filter");
    else url.searchParams.set("filter", next);
    window.history.replaceState({}, "", url.toString());
  }, []);

  const act = useCallback(
    async (id: string, decision: "approve" | "reject", reason?: string) => {
      setBusyId(id);
      try {
        await fetch(`/api/action-requests/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ decision, reason }),
        });
        await load();
      } finally {
        setBusyId(null);
      }
    },
    [load],
  );

  const beginReject = useCallback((id: string) => {
    setRejectingId(id);
    setRejectReason("");
  }, []);

  const cancelReject = useCallback(() => {
    setRejectingId(null);
    setRejectReason("");
  }, []);

  const submitReject = useCallback(async () => {
    if (!rejectingId) return;
    const id = rejectingId;
    const reason = rejectReason.trim() || undefined;
    setRejectingId(null);
    setRejectReason("");
    await act(id, "reject", reason);
  }, [rejectingId, rejectReason, act]);

  useEffect(() => {
    if (filter !== "awaiting") return;
    const handler = (e: KeyboardEvent) => {
      if (!focusedId) return;
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
      if (e.key === "a") {
        e.preventDefault();
        void act(focusedId, "approve");
      } else if (e.key === "r") {
        e.preventDefault();
        beginReject(focusedId);
      } else if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault();
        const ids = data?.actions.map((a) => a.id) ?? [];
        const idx = ids.indexOf(focusedId);
        if (idx >= 0 && idx < ids.length - 1) setFocusedId(ids[idx + 1]);
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        const ids = data?.actions.map((a) => a.id) ?? [];
        const idx = ids.indexOf(focusedId);
        if (idx > 0) setFocusedId(ids[idx - 1]);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [filter, focusedId, data, act, beginReject]);

  useEffect(() => {
    if (!focusedId) return;
    rowRefs.current[focusedId]?.scrollIntoView({
      behavior: "smooth",
      block: "nearest",
    });
  }, [focusedId]);

  const groups = useMemo(
    () => (data ? groupActions(data.actions) : []),
    [data],
  );

  const kbd = "font-mono bg-neutral border border-border rounded-[4px] px-1.5 py-px text-[11px] text-text2";

  return (
    <>
      <div className="root approvals-root">
        <AppHeader>
          <SectionNav current="actions" />
        </AppHeader>

        <div className="flex items-baseline gap-3.5 my-2">
          <h1 className="font-display text-[30px] font-extrabold tracking-[-0.02em] text-text">Actions</h1>
          {data && filter === "awaiting" && (
            <span className="font-mono text-[13px] text-text3">{data.count} pending</span>
          )}
        </div>

        <div className="flex gap-1 mt-1 mb-[18px] border-b border-border">
          {TABS.map((t) => {
            const active = filter === t.key;
            return (
              <button
                key={t.key}
                type="button"
                className={cn(
                  "font-body text-[13px] font-semibold px-3.5 py-2 -mb-px border-b-2 transition-[color,border-color] duration-[120ms] ease-out cursor-pointer",
                  active
                    ? "text-text border-accent"
                    : "text-text3 border-transparent hover:text-text2",
                )}
                onClick={() => switchFilter(t.key)}
              >
                {t.label}
              </button>
            );
          })}
        </div>

        {filter === "awaiting" && data && data.actions.length > 0 && (
          <div className="font-mono text-[12px] text-text3 mb-6">
            <kbd className={kbd}>a</kbd> approve · <kbd className={kbd}>r</kbd> reject · <kbd className={kbd}>j</kbd>/<kbd className={kbd}>k</kbd> navigate
          </div>
        )}

        {error ? (
          <div className="py-[60px] text-center text-danger text-[14px]">{error}</div>
        ) : data === null ? (
          <div className="py-[60px] text-center text-text3 text-[14px]">Loading…</div>
        ) : data.actions.length === 0 ? (
          <EmptyState filter={filter} onBack={() => router.push("/")} />
        ) : (
          <div className="act-list">
            {groups.map((group, i) => {
              const cardData: ActCardData = {
                runId: group.runId,
                runAt: group.runAt,
                trigger: group.trigger,
                state: group.state,
                workflowName: group.workflowName,
                rows: group.rows.map<ActRowData>((r) => ({
                  id: r.id,
                  tone: rowToneFor(r),
                  headline: r.summary || r.kind,
                  detail: null,
                  target: r.target,
                  rejectionReason:
                    r.status === "rejected" ? r.rejectionReason : null,
                  approverPhrase: approverPhrase(r.approverKind, r.approverLabel),
                  status: r.status,
                  minutesSaved: r.minutesSaved,
                })),
              };

              return (
                <ActCard
                  key={group.key}
                  data={cardData}
                  index={i}
                  focusedRowId={focusedId}
                  busyRowId={busyId}
                  rejectingRowId={rejectingId}
                  rejectReason={rejectReason}
                  onRejectReasonChange={setRejectReason}
                  onCancelReject={cancelReject}
                  onSubmitReject={submitReject}
                  onFocusRow={setFocusedId}
                  onApproveRow={(id) => act(id, "approve")}
                  onBeginRejectRow={beginReject}
                  rowRef={(id, el) => {
                    rowRefs.current[id] = el;
                  }}
                />
              );
            })}
          </div>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function EmptyState({ filter, onBack }: { filter: Filter; onBack: () => void }) {
  const copy =
    filter === "awaiting"
      ? {
          line: "Nothing's waiting.",
          sub: "The loop is humming. Anything that needs your judgment will show up here automatically.",
        }
      : filter === "fired"
        ? {
            line: "No actions have fired yet.",
            sub: "When a workflow proposes an action and your rules approve it, it'll land here.",
          }
        : filter === "rejected"
          ? {
              line: "Nothing rejected.",
              sub: "Rejected and failed actions land here so you have an audit trail.",
            }
          : {
              line: "No actions yet.",
              sub: "Workflows haven't proposed anything yet. Once they do, the receipts live here.",
            };
  return (
    <div className="py-20 px-5 text-center text-text3">
      <p className="font-display text-2xl font-bold text-text tracking-[-0.01em] mb-2">{copy.line}</p>
      <p className="text-[14px] leading-[1.5] max-w-[400px] mx-auto mb-6">{copy.sub}</p>
      <button
        type="button"
        className="bg-transparent border-0 text-accent [font:inherit] text-[13px] cursor-pointer p-0 hover:underline hover:[text-underline-offset:3px]"
        onClick={onBack}
      >
        ← Back to dashboard
      </button>
    </div>
  );
}
