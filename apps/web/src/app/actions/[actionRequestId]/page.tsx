"use client";

import { useCallback, useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { cn } from "@/lib/cn";

function actionStatusClasses(status: string): string {
  switch (status) {
    case "pending_approval":
      return "bg-watch-soft text-warn-ink";
    case "rejected":
    case "failed":
      return "bg-danger-soft text-danger";
    case "executed":
    case "succeeded":
      return "bg-success-soft text-success-mid";
    default:
      return "bg-accent-soft text-accent";
  }
}

function actionRiskBoxClasses(risk: string): string {
  switch (risk) {
    case "low":
      return "bg-success-soft text-success-mid";
    case "medium":
      return "bg-watch-soft text-warn-ink";
    case "high":
    case "critical":
      return "bg-danger-soft text-danger";
    default:
      return "bg-accent-soft text-accent";
  }
}

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

function backToActionsHref(status: string): string {
  if (status === "pending_approval") return "/actions?filter=awaiting";
  if (status === "rejected" || status === "failed") return "/actions?filter=rejected";
  if (status === "executed" || status === "approved") return "/actions?filter=fired";
  return "/actions";
}

function backToActionsLabel(status: string): string {
  if (status === "pending_approval") return "Awaiting";
  if (status === "rejected" || status === "failed") return "Rejected";
  if (status === "executed" || status === "approved") return "Fired";
  return "Actions";
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
          <SectionNav current="actions" />
        </AppHeader>
        <div className="py-[60px] text-center text-sm text-danger">{error}</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="actions" />
        </AppHeader>
        <div className="py-[60px] text-center text-sm text-text3">Loading…</div>
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
          <SectionNav current="actions" />
        </AppHeader>

        <div className="mt-1 mb-3.5 font-mono text-[12.5px] text-text3">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-inherit p-0 hover:text-accent"
            onClick={() => router.push(backToActionsHref(ar.status))}
          >
            ← {backToActionsLabel(ar.status)}
          </button>
        </div>

        <div className="mb-[18px]">
          <div className="flex items-start justify-between gap-4 mb-1.5">
            <h1 className="font-display text-[26px] font-extrabold tracking-[-0.02em] text-text">{ar.summary || ar.kind}</h1>
            <button
              type="button"
              className="shrink-0 mt-1 px-3.5 py-[7px] rounded-full border-[1.5px] border-border bg-white/60 font-body text-[12.5px] font-semibold text-text2 cursor-pointer transition hover:border-accent hover:text-accent hover:bg-accent-soft"
              onClick={askFollowUp}
              title="Open an Ask thread pre-loaded with this action's context"
            >
              Ask a follow-up →
            </button>
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-text2">
            <span className={cn(
              "inline-block px-2 py-0.5 rounded-full text-[11.5px] font-semibold tracking-[0.04em] uppercase",
              actionStatusClasses(ar.status),
            )}>
              {statusLabel(ar.status)}
            </span>
            <span className="text-text3/70">·</span>
            <span className="font-mono">{ar.kind}</span>
            {ar.target && (
              <>
                <span className="text-text3/70">·</span>
                <span className="font-mono">{ar.target}</span>
              </>
            )}
            {ar.riskLevel && (
              <>
                <span className="text-text3/70">·</span>
                <span className={cn(
                  "inline-block px-[7px] py-px rounded-md text-[11.5px] font-semibold uppercase tracking-[0.04em]",
                  actionRiskBoxClasses(ar.riskLevel),
                )}>
                  risk {ar.riskLevel}
                </span>
              </>
            )}
            <span className="text-text3/70">·</span>
            <span className="font-mono">{formatRelative(ar.createdAt)}</span>
          </div>
        </div>

        <Section title="Receipt">
          <dl className="grid gap-3.5 m-0">
            <Field label="Proposed">
              <span className="font-mono">{formatTime(ar.createdAt)}</span>
              {workflow && (
                <>
                  <span className="text-text3/70"> · </span>
                  <span>
                    by workflow{" "}
                    <button
                      type="button"
                      className="bg-transparent border-0 cursor-pointer font-inherit p-0 font-semibold text-text underline underline-offset-2 hover:text-accent"
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
                  <span className="font-mono">{formatTime(ar.approvedAt)}</span>
                  <span className="text-text3/70"> · </span>
                  {approverKind === "operator" && (
                    <span>
                      by operator{" "}
                      <span className="font-mono">{ar.approvedByUserId}</span>
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
                <span className="text-text3 italic">
                  Rejected
                  {ar.rejectionReason ? `: ${ar.rejectionReason}` : ""}
                </span>
              ) : (
                <span className="text-text3 italic">awaiting decision</span>
              )}
            </Field>

            <Field label="Executor">
              {latestExecution ? (
                <>
                  <span className="font-mono">{latestExecution.executor}</span>
                  <span className="text-text3/70"> · </span>
                  <span className={cn(
                    "inline-block px-2 py-0.5 rounded-full text-[11.5px] font-semibold tracking-[0.04em] uppercase",
                    actionStatusClasses(latestExecution.status),
                  )}>
                    {latestExecution.status}
                  </span>
                  {latestExecution.finishedAt && (
                    <>
                      <span className="text-text3/70"> · </span>
                      <span className="font-mono">
                        {formatTime(latestExecution.finishedAt)}
                      </span>
                    </>
                  )}
                  {latestExecution.error && (
                    <div className="mt-1.5 px-2.5 py-2 bg-danger-soft text-danger rounded-lg font-mono text-[12.5px]">{latestExecution.error}</div>
                  )}
                </>
              ) : (
                <span className="text-text3 italic">not yet executed</span>
              )}
            </Field>

            <Field label="Payload">
              <button
                type="button"
                className="bg-transparent border-0 p-0 font-inherit text-accent underline underline-offset-2 cursor-pointer"
                onClick={() => setShowPayload((s) => !s)}
              >
                {showPayload ? "hide" : "show"} JSON
              </button>
              {showPayload && (
                <pre className="mt-2 px-3.5 py-3 bg-card border border-border rounded-[10px] font-mono text-[12.5px] text-text2 whitespace-pre-wrap break-words overflow-x-auto">
                  {JSON.stringify(ar.payload, null, 2)}
                </pre>
              )}
            </Field>
          </dl>
        </Section>

        {(workflow || upstreamOutput) && (
          <Section title="Lineage">
            {workflow && ar.workflowRunId && (
              <p className="m-0 mb-2 text-[13.5px] text-text2 leading-[1.55]">
                Proposed by workflow{" "}
                <button
                  type="button"
                  className="bg-transparent border-0 cursor-pointer font-inherit p-0 font-semibold text-text underline underline-offset-2 hover:text-accent"
                  onClick={() => router.push(`/runs/${ar.workflowRunId}`)}
                >
                  {workflow.name}
                </button>{" "}
                — open the run →
              </p>
            )}
            {upstreamOutput && (
              <p className="m-0 mb-2 text-[13.5px] text-text2 leading-[1.55]">
                Triggered by finding{" "}
                <em>{upstreamOutput.title}</em>
                {upstreamOutput.workflowRunId && (
                  <>
                    {" "}in{" "}
                    <button
                      type="button"
                      className="bg-transparent border-0 cursor-pointer font-inherit p-0 font-semibold text-text underline underline-offset-2 hover:text-accent"
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
              <div className="pt-3 border-t border-border mt-2.5 flex flex-col gap-2">
                <label className="text-[11px] font-bold tracking-[0.13em] uppercase text-text3">
                  Why are you rejecting this? (optional)
                </label>
                <textarea
                  className="border border-border rounded-[10px] px-3 py-2 font-body text-[13px] text-text bg-card resize-y min-h-[50px] outline-none focus:border-accent"
                  value={rejectReason}
                  placeholder="e.g. wrong channel, retry tomorrow…"
                  onChange={(e) => setRejectReason(e.target.value)}
                  autoFocus
                  rows={2}
                />
                <div className="flex gap-2 mt-2.5">
                  <button
                    type="button"
                    className="px-3.5 py-[7px] rounded-[10px] border border-danger bg-danger text-white font-body text-[13px] font-semibold cursor-pointer hover:enabled:bg-[#c84545] hover:enabled:border-[#c84545] disabled:opacity-55 disabled:cursor-not-allowed"
                    disabled={busy}
                    onClick={() => void submitReject()}
                  >
                    Confirm reject
                  </button>
                  <button
                    type="button"
                    className="px-3.5 py-[7px] rounded-[10px] border border-border bg-card text-text font-body text-[13px] font-semibold cursor-pointer hover:enabled:border-text3 disabled:opacity-55 disabled:cursor-not-allowed"
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
              <div className="flex gap-2 mt-2.5">
                <button
                  type="button"
                  className="px-3.5 py-[7px] rounded-[10px] border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:enabled:bg-[#5a4cd1] hover:enabled:border-[#5a4cd1] disabled:opacity-55 disabled:cursor-not-allowed"
                  disabled={busy}
                  onClick={() => void submitDecision("approve")}
                >
                  Approve
                </button>
                <button
                  type="button"
                  className="px-3.5 py-[7px] rounded-[10px] border border-border bg-card text-text font-body text-[13px] font-semibold cursor-pointer hover:enabled:border-text3 disabled:opacity-55 disabled:cursor-not-allowed"
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
    <div className="mb-7">
      <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-2.5">{title}</div>
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
    <div className="grid grid-cols-[110px_1fr] gap-x-4 gap-y-2.5 items-baseline">
      <dt className="text-[11.5px] font-bold tracking-[0.13em] uppercase text-text3 m-0">{label}</dt>
      <dd className="m-0 text-[13.5px] text-text2 leading-[1.55]">{children}</dd>
    </div>
  );
}
