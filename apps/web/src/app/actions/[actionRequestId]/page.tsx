"use client";

import { useCallback, useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { cn } from "@/lib/cn";
import {
  parseRecordUpdatePayload,
  RecordActionDiff,
} from "@/components/records/RecordActionDiff";
import { Button } from "@/components/ui/Button";

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
    const initialLoadId = window.setTimeout(() => {
      void load();
    }, 0);
    return () => window.clearTimeout(initialLoadId);
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
  const recordUpdate = parseRecordUpdatePayload(ar.kind, ar.payload);

  return (
    <>
      <div className="root run-root">
        <AppHeader
          back={{
            href: backToActionsHref(ar.status),
            label: backToActionsLabel(ar.status),
          }}
        >
          <SectionNav current="actions" />
        </AppHeader>

        <PageHeading
          title={ar.summary || ar.kind}
          meta={statusLabel(ar.status)}
          description={[
            ar.kind,
            ar.target,
            ar.riskLevel ? `risk ${ar.riskLevel}` : null,
            formatRelative(ar.createdAt),
          ]
            .filter(Boolean)
            .join(" · ")}
          actions={
            <Button
              size="sm"
              className="shrink-0"
              onClick={askFollowUp}
              title="Open an Ask thread pre-loaded with this action's context"
            >
              Ask a follow-up →
            </Button>
          }
        />

        {recordUpdate && (
          <Section title="Proposed change">
            <RecordActionDiff
              kind={ar.kind}
              payload={ar.payload}
              policyContext={isPending && policy ? `rule "${policy.name}"` : null}
            />
          </Section>
        )}

        <Section title="Receipt">
          <dl className="grid gap-3.5 m-0">
            <Field label="Proposed">
              <span className="font-mono">{formatTime(ar.createdAt)}</span>
              {workflow && (
                <>
                  <span className="text-text3/70"> · </span>
                  <span>
                    by workflow{" "}
                    <button data-ui-bespoke-reason="action receipt controls"
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
                    "inline-block px-2 py-0.5 rounded-full text-ui-caption font-semibold tracking-[0.04em] uppercase",
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
                    <div className="mt-1.5 px-2.5 py-2 bg-danger-soft text-danger rounded-lg font-mono text-ui-body-sm">{latestExecution.error}</div>
                  )}
                </>
              ) : (
                <span className="text-text3 italic">not yet executed</span>
              )}
            </Field>

            <Field label="Payload">
              <button data-ui-bespoke-reason="action receipt controls"
                type="button"
                className="bg-transparent border-0 p-0 font-inherit text-accent underline underline-offset-2 cursor-pointer"
                onClick={() => setShowPayload((s) => !s)}
              >
                {showPayload ? "hide" : "show"} JSON
              </button>
              {showPayload && (
                <pre className="mt-2 px-3.5 py-3 bg-card border border-border rounded-[10px] font-mono text-ui-body-sm text-text2 whitespace-pre-wrap break-words overflow-x-auto">
                  {JSON.stringify(ar.payload, null, 2)}
                </pre>
              )}
            </Field>
          </dl>
        </Section>

        {(workflow || upstreamOutput) && (
          <Section title="Lineage">
            {workflow && ar.workflowRunId && (
              <p className="m-0 mb-2 text-ui-body text-text2 leading-[1.55]">
                Proposed by workflow{" "}
                <button data-ui-bespoke-reason="action receipt controls"
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
              <p className="m-0 mb-2 text-ui-body text-text2 leading-[1.55]">
                Triggered by finding{" "}
                <em>{upstreamOutput.title}</em>
                {upstreamOutput.workflowRunId && (
                  <>
                    {" "}in{" "}
                    <button data-ui-bespoke-reason="action receipt controls"
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
                <label className="text-ui-label font-bold tracking-[0.13em] uppercase text-text3">
                  Why are you rejecting this? (optional)
                </label>
                <textarea data-ui-bespoke-reason="action receipt controls"
                  className="border border-border rounded-[10px] px-3 py-2 font-body text-ui-body-sm text-text bg-card resize-y min-h-[50px] outline-none focus:border-accent"
                  value={rejectReason}
                  placeholder="e.g. wrong channel, retry tomorrow…"
                  onChange={(e) => setRejectReason(e.target.value)}
                  autoFocus
                  rows={2}
                />
                <div className="flex gap-2 mt-2.5">
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={busy}
                    onClick={() => void submitReject()}
                  >
                    Confirm reject
                  </Button>
                  <Button
                    size="sm"
                    disabled={busy}
                    onClick={() => {
                      setRejecting(false);
                      setRejectReason("");
                    }}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            ) : (
              <div className="flex gap-2 mt-2.5">
                <Button
                  variant="primary"
                  size="sm"
                  disabled={busy}
                  onClick={() => void submitDecision("approve")}
                >
                  Approve
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={busy}
                  onClick={() => setRejecting(true)}
                >
                  Reject
                </Button>
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
      <div className="text-ui-label font-bold tracking-[0.13em] uppercase text-text3 mb-2.5">{title}</div>
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
      <dt className="text-ui-caption font-bold tracking-[0.13em] uppercase text-text3 m-0">{label}</dt>
      <dd className="m-0 text-ui-body text-text2 leading-[1.55]">{children}</dd>
    </div>
  );
}
