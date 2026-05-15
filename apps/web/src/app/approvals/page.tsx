"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

type ApprovalRow = {
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
  runAt: string;
  createdAt: string;
};

type ApprovalsPayload = {
  approvals: ApprovalRow[];
  count: number;
};

function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-IN", {
    hour: "numeric",
    minute: "2-digit",
    hour12: true,
  });
}

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime();
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return new Date(iso).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
  });
}

function pretty(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export default function ApprovalsPage() {
  const router = useRouter();
  const [data, setData] = useState<ApprovalsPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [focusedId, setFocusedId] = useState<string | null>(null);
  const [rejectingId, setRejectingId] = useState<string | null>(null);
  const [rejectReason, setRejectReason] = useState("");
  const rowRefs = useRef<Record<string, HTMLLIElement | null>>({});

  const load = useCallback(async () => {
    try {
      const res = await fetch("/api/approvals", { cache: "no-store" });
      if (!res.ok) {
        setError(`Couldn't load (HTTP ${res.status})`);
        return;
      }
      const json = (await res.json()) as ApprovalsPayload;
      setData(json);
      if (!focusedId && json.approvals[0]) {
        setFocusedId(json.approvals[0].id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load");
    }
  }, [focusedId]);

  useEffect(() => {
    void load();
  }, [load]);

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

  // Keyboard shortcuts on focused row.
  useEffect(() => {
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
        const ids = data?.approvals.map((a) => a.id) ?? [];
        const idx = ids.indexOf(focusedId);
        if (idx >= 0 && idx < ids.length - 1) {
          setFocusedId(ids[idx + 1]);
        }
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        const ids = data?.approvals.map((a) => a.id) ?? [];
        const idx = ids.indexOf(focusedId);
        if (idx > 0) {
          setFocusedId(ids[idx - 1]);
        }
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [focusedId, data, act, beginReject]);

  // Scroll focused row into view.
  useEffect(() => {
    if (!focusedId) return;
    rowRefs.current[focusedId]?.scrollIntoView({
      behavior: "smooth",
      block: "nearest",
    });
  }, [focusedId]);

  return (
    <>
      <div className="root approvals-root">
        <AppHeader>
          <SectionNav current="approvals" />
        </AppHeader>

        {error ? (
          <div className="approvals-error">{error}</div>
        ) : data === null ? (
          <div className="approvals-loading">Loading…</div>
        ) : data.approvals.length === 0 ? (
          <EmptyState onBack={() => router.push("/")} />
        ) : (
          <>
            <div className="approvals-head">
              <h1 className="approvals-title">Approvals</h1>
              <span className="approvals-count">
                {data.count} pending
              </span>
            </div>

            <div className="approvals-hint">
              <kbd>a</kbd> approve · <kbd>r</kbd> reject · <kbd>j</kbd>/<kbd>k</kbd> navigate
            </div>

            <ul className="approvals-list">
              {data.approvals.map((row) => (
                <ApprovalCard
                  key={row.id}
                  row={row}
                  focused={focusedId === row.id}
                  busy={busyId === row.id}
                  onFocus={() => setFocusedId(row.id)}
                  onApprove={() => act(row.id, "approve")}
                  onReject={() => beginReject(row.id)}
                  rejecting={rejectingId === row.id}
                  rejectReason={rejectReason}
                  onRejectReasonChange={setRejectReason}
                  onCancelReject={cancelReject}
                  onSubmitReject={submitReject}
                  onOpenRun={() =>
                    router.push(`/runs/${row.workflowRunId}`)
                  }
                  onOpenAction={() => router.push(`/actions/${row.id}`)}
                  rowRef={(el) => {
                    rowRefs.current[row.id] = el;
                  }}
                />
              ))}
            </ul>
          </>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function ApprovalCard({
  row,
  focused,
  busy,
  onFocus,
  onApprove,
  onReject,
  onOpenRun,
  onOpenAction,
  rejecting,
  rejectReason,
  onRejectReasonChange,
  onCancelReject,
  onSubmitReject,
  rowRef,
}: {
  row: ApprovalRow;
  focused: boolean;
  busy: boolean;
  onFocus: () => void;
  onApprove: () => void;
  onReject: () => void;
  onOpenRun: () => void;
  onOpenAction: () => void;
  rejecting: boolean;
  rejectReason: string;
  onRejectReasonChange: (value: string) => void;
  onCancelReject: () => void;
  onSubmitReject: () => void;
  rowRef: (el: HTMLLIElement | null) => void;
}) {
  return (
    <li
      ref={rowRef}
      className={`approval-card${focused ? " is-focused" : ""}`}
      onClick={onFocus}
    >
      <div className="approval-card-head">
        <button
          type="button"
          className="approval-card-title approval-card-title-link"
          onClick={(e) => {
            e.stopPropagation();
            onOpenAction();
          }}
          title="Open action receipt"
        >
          {row.summary || `${row.kind}`}
        </button>
        {row.riskLevel && (
          <span
            className={`approval-card-risk approval-card-risk-${row.riskLevel}`}
          >
            {row.riskLevel}
          </span>
        )}
      </div>

      <dl className="approval-card-meta">
        <Field label="Action">
          <span className="approval-mono">{row.kind}</span>
        </Field>
        {row.target && (
          <Field label="Target">
            <span className="approval-mono approval-card-target">
              {row.target}
            </span>
          </Field>
        )}
        <Field label="From">
          <button
            type="button"
            className="approval-card-link"
            onClick={(e) => {
              e.stopPropagation();
              onOpenRun();
            }}
          >
            {row.workflow.name} · {formatTime(row.runAt)} →
          </button>
        </Field>
        {row.triggeredByObservation && (
          <Field label="Triggered">
            by observation &ldquo;{row.triggeredByObservation.title}&rdquo;
          </Field>
        )}
      </dl>

      {row.payload != null && Object.keys(row.payload as object).length > 0 && (
        <div className="approval-card-payload">
          <div className="approval-card-payload-label">Payload</div>
          <pre className="approval-card-payload-body">{pretty(row.payload)}</pre>
        </div>
      )}

      {rejecting ? (
        <div className="approval-reject-box" onClick={(e) => e.stopPropagation()}>
          <label className="approval-reject-label">
            Why are you rejecting this? (optional)
          </label>
          <textarea
            className="approval-reject-textarea"
            value={rejectReason}
            placeholder="e.g. wrong channel, retry tomorrow…"
            onChange={(e) => onRejectReasonChange(e.target.value)}
            autoFocus
            rows={2}
          />
          <div className="approval-reject-actions">
            <button
              type="button"
              className="approval-card-btn is-destructive"
              disabled={busy}
              onClick={onSubmitReject}
            >
              Confirm reject
            </button>
            <button
              type="button"
              className="approval-card-btn"
              disabled={busy}
              onClick={onCancelReject}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="approval-card-actions">
          <button
            type="button"
            className="approval-card-btn is-primary"
            disabled={busy}
            onClick={(e) => {
              e.stopPropagation();
              onApprove();
            }}
          >
            [a] Approve
          </button>
          <button
            type="button"
            className="approval-card-btn"
            disabled={busy}
            onClick={(e) => {
              e.stopPropagation();
              onReject();
            }}
          >
            [r] Reject
          </button>
          <span className="approval-card-when">{formatRelative(row.createdAt)}</span>
        </div>
      )}
    </li>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="approval-field">
      <dt className="approval-field-label">{label}</dt>
      <dd className="approval-field-value">{children}</dd>
    </div>
  );
}

function EmptyState({ onBack }: { onBack: () => void }) {
  return (
    <div className="approvals-empty">
      <p className="approvals-empty-line">Nothing&apos;s waiting.</p>
      <p className="approvals-empty-sub">
        The loop is humming. Anything that needs your judgment will show up
        here automatically.
      </p>
      <button
        type="button"
        className="approvals-empty-btn"
        onClick={onBack}
      >
        ← Back to dashboard
      </button>
    </div>
  );
}
