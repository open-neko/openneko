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
import { formatSavedShort } from "@/lib/hours-saved";

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

        {error ? (
          <div className="py-[60px] text-center text-danger text-[14px]">{error}</div>
        ) : data === null ? (
          <div className="py-[60px] text-center text-text3 text-[14px]">Loading…</div>
        ) : data.actions.length === 0 ? (
          <EmptyState filter={filter} onBack={() => router.push("/")} />
        ) : (
          <div className="triage-layout">
            <div className="act-list triage-queue">
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
            {filter === "awaiting" && (
              <ActionReadingPane
                action={data.actions.find((a) => a.id === focusedId) ?? null}
                busy={busyId !== null && busyId === focusedId}
                onApprove={() => { if (focusedId) void act(focusedId, "approve"); }}
                onReject={() => { if (focusedId) beginReject(focusedId); }}
              />
            )}
          </div>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

const RISK_PILL: Record<string, string> = {
  critical: "bg-danger text-white",
  high: "bg-danger-soft text-danger border border-danger/30",
  medium: "bg-watch-soft text-warn-ink border border-watch/30",
  low: "bg-success-soft text-success-ink border border-success-mid/30",
};

// Reading pane for the triage queue (Compact). Shows the focused action's
// full context — why, target, payload, value — beside the queue list.
function ActionReadingPane({
  action,
  busy,
  onApprove,
  onReject,
}: {
  action: ActionRow | null;
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
}) {
  if (!action) {
    return (
      <aside className="triage-pane">
        <div className="bg-card border border-border rounded-2xl px-5 py-10 text-center text-[13px] text-text3 shadow-soft">
          Select an action to review.
        </div>
      </aside>
    );
  }
  const payloadEntries =
    action.payload && typeof action.payload === "object" && !Array.isArray(action.payload)
      ? Object.entries(action.payload as Record<string, unknown>).slice(0, 8)
      : [];
  const risk = action.riskLevel ?? "low";
  return (
    <aside className="triage-pane">
      <div className="bg-card border border-border rounded-2xl px-5 py-[18px] shadow-soft">
        <div className="flex items-center gap-2.5 mb-2.5">
          <span className={cn("font-display text-[9.5px] font-extrabold tracking-[0.08em] uppercase px-2 py-0.5 rounded-full", RISK_PILL[risk] ?? RISK_PILL.low)}>
            {risk} risk
          </span>
          <code className="ml-auto font-mono text-[11px] text-text3">{action.kind}</code>
        </div>
        <h2 className="font-display text-[19px] font-extrabold tracking-[-0.02em] leading-[1.2] text-text">
          {action.summary || action.kind}
        </h2>
        <div className="text-[12.5px] text-text2 mt-2 leading-[1.5]">
          Proposed by <span className="text-text font-semibold">{action.workflow.name}</span>
          {action.triggeredByObservation ? <> · triggered by “{action.triggeredByObservation.title}”</> : null}
        </div>

        {action.target && (
          <div className="mt-4">
            <div className="text-[10px] font-bold tracking-[0.12em] uppercase text-text3 mb-1.5">Target</div>
            <code className="font-mono text-[12px] text-text2 break-all">{action.target}</code>
          </div>
        )}

        {payloadEntries.length > 0 && (
          <div className="mt-4">
            <div className="text-[10px] font-bold tracking-[0.12em] uppercase text-text3 mb-1.5">Payload</div>
            <div className="bg-bg border border-border rounded-xl px-3 py-2.5 grid gap-1.5">
              {payloadEntries.map(([k, v]) => (
                <div key={k} className="flex gap-3 text-[12px]">
                  <span className="text-text3 min-w-[88px] flex-none">{k}</span>
                  <span className="font-mono text-text break-all">
                    {typeof v === "object" ? JSON.stringify(v) : String(v)}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}

        {(action.minutesSaved ?? 0) > 0 && (
          <div className="mt-4 text-[12.5px] text-text2">
            Saves <span className="font-mono text-success-ink">{formatSavedShort(action.minutesSaved as number)}</span> of manual effort.
          </div>
        )}

        <div className="flex items-center gap-2.5 mt-[18px] pt-4 border-t border-border">
          <button
            type="button"
            disabled={busy}
            onClick={onApprove}
            className="px-[18px] py-2.5 rounded-[11px] bg-success-ink text-white font-display font-bold text-[13.5px] tracking-[-0.01em] hover:bg-[#0b2912] disabled:opacity-50 cursor-pointer"
          >
            Approve
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={onReject}
            className="px-[18px] py-2.5 rounded-[11px] border border-border text-text2 font-semibold text-[13.5px] hover:border-danger hover:text-danger disabled:opacity-50 cursor-pointer"
          >
            Reject
          </button>
        </div>
      </div>
    </aside>
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
