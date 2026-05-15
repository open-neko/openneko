"use client";

import { useCallback, useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

type ActionDetailPayload = {
  actionRequest: {
    id: string;
    workflowRunId: string | null;
    triggeredByObservationId: string | null;
    policyId: string | null;
    scope: string;
    kind: string;
    target: string | null;
    payload: unknown;
    riskLevel: string | null;
    status: string;
    summary: string | null;
    approvedByUserId: string | null;
    approvedAt: string | null;
    rejectionReason: string | null;
    createdAt: string;
    updatedAt: string;
  };
  executions: Array<{
    id: string;
    executor: string;
    commandOrOperation: string | null;
    payload: unknown;
    result: unknown;
    externalRef: string | null;
    status: string;
    error: string | null;
    startedAt: string | null;
    finishedAt: string | null;
    createdAt: string;
  }>;
  workflow: { id: string; name: string } | null;
  policy: { id: string; name: string; mode: string } | null;
  upstreamOutput: { id: string; title: string; workflowRunId: string | null } | null;
  approverKind: "operator" | "policy" | "auto" | null;
};

const STATUS_LABEL: Record<string, string> = {
  pending_approval: "Awaiting you",
  approved: "Approved",
  rejected: "Rejected",
  executed: "Fired",
  failed: "Failed",
};

function statusLabel(s: string): string {
  return STATUS_LABEL[s] ?? s.replace(/_/g, " ");
}

function formatTime(iso: string | null): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString("en-IN", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    hour12: true,
  });
}

function formatRelative(iso: string | null): string {
  if (!iso) return "";
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

export default function ActionPage() {
  const params = useParams<{ actionRequestId: string }>();
  const actionRequestId = params?.actionRequestId;
  const router = useRouter();
  const [data, setData] = useState<ActionDetailPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [rejectReason, setRejectReason] = useState("");
  const [showPayload, setShowPayload] = useState(false);

  const load = useCallback(async () => {
    if (!actionRequestId) return;
    try {
      const res = await fetch(`/api/action-requests/${actionRequestId}`, {
        cache: "no-store",
      });
      if (!res.ok) {
        setError(`Couldn't load action (HTTP ${res.status})`);
        return;
      }
      const json = (await res.json()) as ActionDetailPayload;
      setData(json);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load");
    }
  }, [actionRequestId]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while pending so an auto-approval or executor result lands without
  // reload. Stop once the action reaches a terminal state.
  useEffect(() => {
    const status = data?.actionRequest?.status;
    if (!status) return;
    if (status === "executed" || status === "rejected" || status === "failed") {
      return;
    }
    const id = setInterval(() => {
      void load();
    }, 3000);
    return () => clearInterval(id);
  }, [data?.actionRequest?.status, load]);

  const submitDecision = useCallback(
    async (decision: "approve" | "reject", reason?: string) => {
      if (!actionRequestId) return;
      setBusy(true);
      try {
        await fetch(`/api/action-requests/${actionRequestId}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ decision, reason }),
        });
        await load();
      } finally {
        setBusy(false);
      }
    },
    [actionRequestId, load],
  );

  const submitReject = useCallback(async () => {
    const reason = rejectReason.trim() || undefined;
    setRejecting(false);
    setRejectReason("");
    await submitDecision("reject", reason);
  }, [rejectReason, submitDecision]);

  const askFollowUp = useCallback(async () => {
    if (!actionRequestId) return;
    try {
      const res = await fetch("/api/work/threads", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ seedActionRequestId: actionRequestId }),
      });
      if (!res.ok) return;
      const json = (await res.json()) as { thread: { id: string } };
      if (json.thread?.id) router.push(`/work/${json.thread.id}`);
    } catch {
      // best-effort
    }
  }, [actionRequestId, router]);

  if (error) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="approvals" />
        </AppHeader>
        <div className="run-error">{error}</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="approvals" />
        </AppHeader>
        <div className="run-loading">Loading…</div>
      </div>
    );
  }

  const { actionRequest: ar, executions, workflow, policy, upstreamOutput, approverKind } = data;
  const isPending = ar.status === "pending_approval";
  const latestExecution = executions[0] ?? null;

  return (
    <>
      <div className="root run-root">
        <AppHeader>
          <SectionNav current="approvals" />
        </AppHeader>

        <div className="run-crumb">
          <button
            type="button"
            className="run-crumb-link"
            onClick={() => router.push("/approvals")}
          >
            ← Approvals
          </button>
        </div>

        <div className="run-header">
          <div className="run-header-row">
            <h1 className="run-title">{ar.summary || ar.kind}</h1>
            <button
              type="button"
              className="run-followup-btn"
              onClick={askFollowUp}
              title="Open an Ask thread pre-loaded with this action's context"
            >
              Ask a follow-up →
            </button>
          </div>
          <div className="run-header-meta">
            <span className={`action-status action-status-${ar.status}`}>
              {statusLabel(ar.status)}
            </span>
            <span className="run-sep">·</span>
            <span className="run-mono">{ar.kind}</span>
            {ar.target && (
              <>
                <span className="run-sep">·</span>
                <span className="run-mono">{ar.target}</span>
              </>
            )}
            {ar.riskLevel && (
              <>
                <span className="run-sep">·</span>
                <span className={`action-risk action-risk-${ar.riskLevel}`}>
                  risk {ar.riskLevel}
                </span>
              </>
            )}
            <span className="run-sep">·</span>
            <span className="run-mono">{formatRelative(ar.createdAt)}</span>
          </div>
        </div>

        <Section title="Receipt">
          <dl className="action-receipt">
            <Field label="Proposed">
              <span className="run-mono">{formatTime(ar.createdAt)}</span>
              {workflow && (
                <>
                  <span className="run-sep"> · </span>
                  <span>
                    by workflow{" "}
                    <button
                      type="button"
                      className="run-crumb-link action-inline-link"
                      onClick={() =>
                        ar.workflowRunId &&
                        router.push(`/runs/${ar.workflowRunId}`)
                      }
                    >
                      {workflow.name}
                    </button>
                  </span>
                </>
              )}
            </Field>

            <Field label="Approved">
              {ar.approvedAt ? (
                <>
                  <span className="run-mono">{formatTime(ar.approvedAt)}</span>
                  <span className="run-sep"> · </span>
                  {approverKind === "operator" && (
                    <span>
                      by operator{" "}
                      <span className="run-mono">{ar.approvedByUserId}</span>
                    </span>
                  )}
                  {approverKind === "policy" && policy && (
                    <span>
                      by rule <strong>{policy.name}</strong> ({policy.mode})
                    </span>
                  )}
                  {approverKind === "auto" && <span>automatically</span>}
                </>
              ) : ar.status === "rejected" ? (
                <span className="run-empty-inline">
                  Rejected
                  {ar.rejectionReason ? `: ${ar.rejectionReason}` : ""}
                </span>
              ) : (
                <span className="run-empty-inline">awaiting decision</span>
              )}
            </Field>

            <Field label="Executor">
              {latestExecution ? (
                <>
                  <span className="run-mono">{latestExecution.executor}</span>
                  <span className="run-sep"> · </span>
                  <span className={`action-status action-status-${latestExecution.status}`}>
                    {latestExecution.status}
                  </span>
                  {latestExecution.finishedAt && (
                    <>
                      <span className="run-sep"> · </span>
                      <span className="run-mono">
                        {formatTime(latestExecution.finishedAt)}
                      </span>
                    </>
                  )}
                  {latestExecution.error && (
                    <div className="action-error">{latestExecution.error}</div>
                  )}
                </>
              ) : (
                <span className="run-empty-inline">not yet executed</span>
              )}
            </Field>

            <Field label="Payload">
              <button
                type="button"
                className="action-payload-toggle"
                onClick={() => setShowPayload((s) => !s)}
              >
                {showPayload ? "hide" : "show"} JSON
              </button>
              {showPayload && (
                <pre className="action-payload">
                  {JSON.stringify(ar.payload, null, 2)}
                </pre>
              )}
            </Field>
          </dl>
        </Section>

        {(workflow || upstreamOutput) && (
          <Section title="Lineage">
            {workflow && ar.workflowRunId && (
              <p className="action-lineage-line">
                Proposed by workflow{" "}
                <button
                  type="button"
                  className="run-crumb-link action-inline-link"
                  onClick={() => router.push(`/runs/${ar.workflowRunId}`)}
                >
                  {workflow.name}
                </button>{" "}
                — open the run →
              </p>
            )}
            {upstreamOutput && (
              <p className="action-lineage-line">
                Triggered by finding{" "}
                <em>{upstreamOutput.title}</em>
                {upstreamOutput.workflowRunId && (
                  <>
                    {" "}in{" "}
                    <button
                      type="button"
                      className="run-crumb-link action-inline-link"
                      onClick={() =>
                        router.push(`/runs/${upstreamOutput.workflowRunId}`)
                      }
                    >
                      its run
                    </button>
                  </>
                )}
              </p>
            )}
          </Section>
        )}

        {isPending && (
          <Section title="Decide">
            {rejecting ? (
              <div className="run-action-reject-box">
                <label className="run-action-reject-label">
                  Why are you rejecting this? (optional)
                </label>
                <textarea
                  className="run-action-reject-textarea"
                  value={rejectReason}
                  placeholder="e.g. wrong channel, retry tomorrow…"
                  onChange={(e) => setRejectReason(e.target.value)}
                  autoFocus
                  rows={2}
                />
                <div className="run-action-buttons">
                  <button
                    type="button"
                    className="run-action-btn is-destructive"
                    disabled={busy}
                    onClick={() => void submitReject()}
                  >
                    Confirm reject
                  </button>
                  <button
                    type="button"
                    className="run-action-btn"
                    disabled={busy}
                    onClick={() => {
                      setRejecting(false);
                      setRejectReason("");
                    }}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            ) : (
              <div className="run-action-buttons">
                <button
                  type="button"
                  className="run-action-btn is-primary"
                  disabled={busy}
                  onClick={() => void submitDecision("approve")}
                >
                  Approve
                </button>
                <button
                  type="button"
                  className="run-action-btn"
                  disabled={busy}
                  onClick={() => setRejecting(true)}
                >
                  Reject
                </button>
              </div>
            )}
          </Section>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="run-section">
      <div className="run-section-title">{title}</div>
      {children}
    </div>
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
    <div className="action-field">
      <dt className="action-field-label">{label}</dt>
      <dd className="action-field-value">{children}</dd>
    </div>
  );
}
