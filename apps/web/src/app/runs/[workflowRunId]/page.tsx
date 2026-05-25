"use client";

import { useCallback, useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { cn } from "@/lib/cn";

type RunDetailPayload = {
  workflow: {
    id: string;
    name: string;
    description: string;
    goal: string;
  } | null;
  run: {
    id: string;
    workflowId: string;
    threadId: string;
    workRunId: string | null;
    triggerKind: string;
    triggerPayload: unknown;
    chainDepth: number;
    status: string;
    summary: string | null;
    error: string | null;
    startedAt: string | null;
    finishedAt: string | null;
    createdAt: string;
    updatedAt: string;
  };
  outputs: Array<{
    id: string;
    kind: string;
    title: string;
    body: string | null;
    payload: unknown;
    scope: string | null;
    topic: string | null;
    mood: string | null;
    createdAt: string;
  }>;
  actions: Array<{
    id: string;
    kind: string;
    target: string | null;
    payload: unknown;
    scope: string;
    riskLevel: string | null;
    status: string;
    summary: string;
    approvedAt: string | null;
    rejectionReason: string | null;
    createdAt: string;
  }>;
  events: Array<{
    seq: number;
    type: string;
    event: Record<string, unknown> | null;
    createdAt: string;
  }>;
  lineage: {
    triggeredBySubscriptionId: string | null;
    triggeredByOutputId: string | null;
    triggeredByObservationId: string | null;
    upstream: null | {
      output: {
        id: string;
        title: string;
        scope: string | null;
        mood: string | null;
        createdAt: string;
      };
      workflow: { id: string; name: string } | null;
      workflowRunId: string | null;
    };
  };
};

function formatTime(iso: string | null): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleTimeString("en-IN", {
    hour: "numeric",
    minute: "2-digit",
    hour12: true,
  });
}

function formatDuration(ms: number | null): string {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

const ACTION_STATUS_LABEL: Record<string, string> = {
  pending_approval: "Awaiting you",
  approved: "Approved",
  rejected: "Rejected",
  executed: "Fired",
  failed: "Failed",
};

function actionStatusLabel(s: string): string {
  return ACTION_STATUS_LABEL[s] ?? s.replace(/_/g, " ");
}

function runStatusClasses(status: string): string {
  switch (status) {
    case "completed":
      return "bg-success-soft text-success-mid";
    case "failed":
      return "bg-danger-soft text-danger";
    case "running":
    case "queued":
    case "needs_input":
    case "waiting_approval":
      return "bg-watch-soft text-warn-ink";
    default:
      return "bg-neutral text-text2";
  }
}

function moodClasses(mood: string): string {
  switch (mood) {
    case "good":
      return "bg-success-soft text-success-mid";
    case "watch":
      return "bg-watch-soft text-warn-ink";
    case "act":
      return "bg-danger-soft text-danger";
    default:
      return "bg-neutral text-text2";
  }
}

function actionPillClasses(status: string): string {
  switch (status) {
    case "pending_approval":
      return "bg-watch-soft text-warn-ink";
    case "executed":
      return "bg-success-soft text-success-mid";
    case "rejected":
    case "failed":
      return "bg-danger-soft text-danger";
    case "approved":
      return "bg-accent-soft text-accent";
    default:
      return "bg-neutral text-text2";
  }
}

function actionRiskClasses(risk: string): string {
  switch (risk) {
    case "low":
      return "text-text2";
    case "medium":
      return "text-warn-ink";
    case "high":
    case "critical":
      return "text-danger";
    default:
      return "text-text2";
  }
}

function formatTrigger(kind: string): string {
  switch (kind) {
    case "manual":
      return "manual";
    case "cron":
      return "cron";
    case "subscription":
      return "subscription";
    default:
      return kind;
  }
}

export default function RunPage() {
  const params = useParams<{ workflowRunId: string }>();
  const workflowRunId = params?.workflowRunId;
  const router = useRouter();
  const [data, setData] = useState<RunDetailPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [showEvents, setShowEvents] = useState(false);
  const [showLineage, setShowLineage] = useState(false);
  const [actionBusyId, setActionBusyId] = useState<string | null>(null);
  const [rejectingId, setRejectingId] = useState<string | null>(null);
  const [rejectReason, setRejectReason] = useState("");

  const load = useCallback(async () => {
    if (!workflowRunId) return;
    try {
      const res = await fetch(`/api/workflow-runs/${workflowRunId}`, {
        cache: "no-store",
      });
      if (!res.ok) {
        setError(`Couldn't load run (HTTP ${res.status})`);
        return;
      }
      const json = (await res.json()) as RunDetailPayload;
      setData(json);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load");
    }
  }, [workflowRunId]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while the run isn't terminal so users can watch live progress.
  useEffect(() => {
    const status = data?.run?.status;
    if (!status) return;
    if (status === "completed" || status === "failed" || status === "cancelled") {
      return;
    }
    const id = setInterval(() => {
      void load();
    }, 3000);
    return () => clearInterval(id);
  }, [data?.run?.status, load]);

  const actOnRequest = useCallback(
    async (id: string, decision: "approve" | "reject", reason?: string) => {
      setActionBusyId(id);
      try {
        await fetch(`/api/action-requests/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ decision, reason }),
        });
        await load();
      } finally {
        setActionBusyId(null);
      }
    },
    [load],
  );

  const submitReject = useCallback(async () => {
    if (!rejectingId) return;
    const id = rejectingId;
    const reason = rejectReason.trim() || undefined;
    setRejectingId(null);
    setRejectReason("");
    await actOnRequest(id, "reject", reason);
  }, [rejectingId, rejectReason, actOnRequest]);

  const cancelReject = useCallback(() => {
    setRejectingId(null);
    setRejectReason("");
  }, []);

  const pinOutput = useCallback(async (outputId: string) => {
    await fetch("/api/briefing/pins", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ outputId }),
    }).catch(() => {});
    // No need to refetch — the page doesn't show "is pinned" state for
    // this output today. The pinned state lands on the Briefing via the
    // /api/briefing/findings poll.
  }, []);

  const askFollowUp = useCallback(async () => {
    if (!workflowRunId) return;
    try {
      const res = await fetch("/api/work/threads", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ seedWorkflowRunId: workflowRunId }),
      });
      if (!res.ok) return;
      const json = (await res.json()) as { thread: { id: string } };
      if (json.thread?.id) router.push(`/work/${json.thread.id}`);
    } catch {
      // best-effort
    }
  }, [workflowRunId, router]);

  if (error) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>
        <div className="py-[60px] text-center text-sm text-danger">{error}</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>
        <div className="py-[60px] text-center text-sm text-text3">Loading…</div>
      </div>
    );
  }

  const { run, workflow, outputs, actions, lineage } = data;
  const durationMs =
    run.startedAt && run.finishedAt
      ? new Date(run.finishedAt).getTime() - new Date(run.startedAt).getTime()
      : null;

  return (
    <>
      <div className="root run-root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>

        <div className="mt-1 mb-3.5 font-mono text-[12.5px] text-text3">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-inherit p-0 hover:text-accent"
            onClick={() => router.push("/workflows")}
          >
            ← Workflows
          </button>
        </div>

        <div className="mb-[18px]">
          <div className="flex items-start justify-between gap-4 mb-1.5">
            <h1 className="font-display text-[26px] font-extrabold tracking-[-0.02em] text-text">{workflow?.name ?? "Run"}</h1>
            <button
              type="button"
              className="shrink-0 mt-1 px-3.5 py-[7px] rounded-full border-[1.5px] border-border bg-white/60 font-body text-[12.5px] font-semibold text-text2 cursor-pointer transition hover:border-accent hover:text-accent hover:bg-accent-soft"
              onClick={askFollowUp}
              title="Open an Ask thread pre-loaded with this run's context"
            >
              Ask a follow-up →
            </button>
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-text2">
            <span className="font-mono">{formatTime(run.startedAt ?? run.createdAt)}</span>
            <span className="text-text3/70">·</span>
            <span>{formatTrigger(run.triggerKind)}</span>
            <span className="text-text3/70">·</span>
            <span className={cn(
              "uppercase text-[10.5px] tracking-[0.13em] font-bold px-2 py-0.5 rounded-full",
              runStatusClasses(run.status),
            )}>
              {run.status}
            </span>
            <span className="text-text3/70">·</span>
            <span className="font-mono">{formatDuration(durationMs)}</span>
          </div>
        </div>

        {run.status === "completed" &&
          outputs.length === 0 &&
          actions.length === 0 && (
            <div className="my-2 mb-[22px] px-[18px] py-3.5 bg-card border border-border rounded-xl text-sm text-text2 italic">
              {run.summary?.trim() || "Looked at the data; nothing to flag."}
            </div>
          )}

        <Section title="Findings">
          {outputs.length === 0 ? (
            <p className="text-text3 text-[13.5px] italic">
              This run hasn't produced any outputs yet.
            </p>
          ) : (
            <ul className="list-none p-0 m-0 flex flex-col gap-3">
              {outputs.map((o) => (
                <li key={o.id} className="bg-card border border-border rounded-2xl px-[18px] py-4">
                  <div className="flex items-center justify-between gap-2 mb-1.5">
                    <div className="font-display text-base font-bold tracking-[-0.01em] text-text">{o.title}</div>
                    {o.mood && (
                      <span className={cn(
                        "text-[10.5px] font-bold tracking-[0.12em] uppercase px-2 py-0.5 rounded-full",
                        moodClasses(o.mood),
                      )}>
                        {o.mood}
                      </span>
                    )}
                  </div>
                  {o.body && (
                    <div className="run-evt-message text-text text-sm leading-[1.55] mb-2">
                      <ReactMarkdown remarkPlugins={[remarkGfm]}>{o.body}</ReactMarkdown>
                    </div>
                  )}
                  <div className="flex items-center gap-1.5 text-[11.5px] text-text3">
                    <span className="font-mono uppercase tracking-[0.08em]">{o.kind}</span>
                    {o.scope && (
                      <>
                        <span className="text-text3/70">·</span>
                        <span>scope: {o.scope}</span>
                      </>
                    )}
                    <button
                      type="button"
                      className="ml-auto bg-transparent border-0 text-text3 font-body text-[11.5px] cursor-pointer p-0 hover:text-accent hover:underline hover:underline-offset-2"
                      onClick={() => pinOutput(o.id)}
                      title="Pin this finding to the Briefing"
                    >
                      pin to briefing
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section title="Actions">
          {actions.length === 0 ? (
            <p className="text-text3 text-[13.5px] italic">No actions proposed by this run.</p>
          ) : (
            <ul className="list-none p-0 m-0 flex flex-col gap-2.5">
              {actions.map((a) => (
                <li key={a.id} className="bg-card border border-border rounded-2xl px-4 py-3.5">
                  <div className="flex items-start justify-between gap-2 mb-1.5">
                    <button
                      type="button"
                      className="font-semibold text-sm text-text leading-[1.4] bg-transparent border-0 p-0 text-left cursor-pointer hover:underline hover:underline-offset-[3px]"
                      onClick={() => router.push(`/actions/${a.id}`)}
                      title="Open action receipt"
                    >
                      {a.summary || a.kind}
                    </button>
                    <span
                      className={cn(
                        "text-[10.5px] font-bold tracking-[0.12em] uppercase px-[9px] py-[3px] rounded-full shrink-0",
                        actionPillClasses(a.status),
                      )}
                    >
                      {actionStatusLabel(a.status)}
                    </span>
                  </div>
                  <div className="flex flex-wrap gap-1.5 items-center text-xs text-text3">
                    <span className="font-mono text-text2">{a.kind}</span>
                    {a.target && (
                      <>
                        <span className="text-text3/70">·</span>
                        <span className="font-mono">{a.target}</span>
                      </>
                    )}
                    {a.riskLevel && (
                      <>
                        <span className="text-text3/70">·</span>
                        <span className={cn("font-semibold", actionRiskClasses(a.riskLevel))}>
                          {a.riskLevel}
                        </span>
                      </>
                    )}
                  </div>
                  {a.status === "pending_approval" && rejectingId === a.id ? (
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
                          disabled={actionBusyId === a.id}
                          onClick={() => void submitReject()}
                        >
                          Confirm reject
                        </button>
                        <button
                          type="button"
                          className="px-3.5 py-[7px] rounded-[10px] border border-border bg-card text-text font-body text-[13px] font-semibold cursor-pointer hover:enabled:border-text3 disabled:opacity-55 disabled:cursor-not-allowed"
                          disabled={actionBusyId === a.id}
                          onClick={cancelReject}
                        >
                          Cancel
                        </button>
                      </div>
                    </div>
                  ) : a.status === "pending_approval" ? (
                    <div className="flex gap-2 mt-2.5">
                      <button
                        type="button"
                        className="px-3.5 py-[7px] rounded-[10px] border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:enabled:bg-[#5a4cd1] hover:enabled:border-[#5a4cd1] disabled:opacity-55 disabled:cursor-not-allowed"
                        disabled={actionBusyId === a.id}
                        onClick={() => void actOnRequest(a.id, "approve")}
                      >
                        Approve
                      </button>
                      <button
                        type="button"
                        className="px-3.5 py-[7px] rounded-[10px] border border-border bg-card text-text font-body text-[13px] font-semibold cursor-pointer hover:enabled:border-text3 disabled:opacity-55 disabled:cursor-not-allowed"
                        disabled={actionBusyId === a.id}
                        onClick={() => setRejectingId(a.id)}
                      >
                        Reject
                      </button>
                    </div>
                  ) : null}
                  {a.status === "rejected" && a.rejectionReason && (
                    <p className="mt-2 text-[12.5px] text-text2 italic">
                      Rejected: {a.rejectionReason}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Section>

        <details
          className="run-expander"
          open={showEvents}
          onToggle={(e) => setShowEvents(e.currentTarget.open)}
        >
          <summary className="run-expander-summary">
            How it got there ({data.events.length} events)
          </summary>
          <div className="mt-3 pl-[18px] text-[13.5px] text-text leading-[1.55]">
            {data.events.length === 0 ? (
              <p className="text-text3 text-[13.5px] italic">No events recorded.</p>
            ) : (
              <EventStream events={data.events} />
            )}
          </div>
        </details>

        <details
          className="run-expander"
          open={showLineage}
          onToggle={(e) => setShowLineage(e.currentTarget.open)}
        >
          <summary className="run-expander-summary">Lineage</summary>
          <div className="mt-3 pl-[18px] text-[13.5px] text-text leading-[1.55]">
            {!lineage.upstream && run.triggerKind === "manual" && (
              <p className="text-text3 text-[13.5px] italic">
                Started manually from the workflow drawer.
              </p>
            )}
            {!lineage.upstream && run.triggerKind === "cron" && (
              <p className="text-text3 text-[13.5px] italic">
                Scheduled by cron · no upstream output.
              </p>
            )}
            {lineage.upstream && (
              <div className="text-[13.5px] leading-[1.55] text-text">
                <p>
                  Triggered by{" "}
                  {lineage.upstream.workflow ? (
                    <>
                      workflow{" "}
                      <strong>{lineage.upstream.workflow.name}</strong>
                    </>
                  ) : (
                    "an upstream workflow"
                  )}{" "}
                  on output{" "}
                  <span className="italic text-text2">
                    &ldquo;{lineage.upstream.output.title}&rdquo;
                  </span>
                </p>
                {lineage.upstream.workflowRunId && (
                  <button
                    type="button"
                    className="mt-2 bg-transparent border-0 text-accent cursor-pointer font-inherit text-[12.5px] p-0 hover:underline hover:underline-offset-[3px]"
                    onClick={() =>
                      router.push(`/runs/${lineage.upstream!.workflowRunId}`)
                    }
                  >
                    open upstream run →
                  </button>
                )}
              </div>
            )}
          </div>
        </details>
      </div>

      <CreatorCredit />
    </>
  );
}

type EventRow = RunDetailPayload["events"][number];

type RenderedItem =
  | { type: "message-block"; seq: number; content: string }
  | { type: "single"; seq: number; ev: EventRow };

function coalesceEvents(events: EventRow[]): RenderedItem[] {
  // Streaming chunks each become their own `message` event row. Rendering each
  // independently through ReactMarkdown breaks any inline syntax that spans a
  // chunk boundary (e.g. `**Daily Revenue Health Check**` split between two
  // events leaves both halves un-bolded). Coalesce consecutive message events
  // into a single block so the markdown parser sees a complete string.
  const items: RenderedItem[] = [];
  let current: { seq: number; content: string } | null = null;
  for (const ev of events) {
    if (ev.type === "message") {
      const content = (ev.event as { content?: string } | null)?.content ?? "";
      if (current) {
        current.content += content;
      } else {
        current = { seq: ev.seq, content };
      }
      continue;
    }
    if (current) {
      items.push({ type: "message-block", seq: current.seq, content: current.content });
      current = null;
    }
    items.push({ type: "single", seq: ev.seq, ev });
  }
  if (current) {
    items.push({ type: "message-block", seq: current.seq, content: current.content });
  }
  return items;
}

function EventStream({ events }: { events: EventRow[] }) {
  const items = coalesceEvents(events);
  return (
    <ul className="list-none p-0 m-0 flex flex-col gap-1.5 text-[13.5px] border-l-2 border-border pl-3">
      {items.map((item) => {
        if (item.type === "message-block") {
          return (
            <li key={item.seq} className="run-evt-message text-text leading-[1.55]">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                {item.content}
              </ReactMarkdown>
            </li>
          );
        }
        const ev = item.ev;
        if (ev.type === "status") {
          const message = (ev.event as { message?: string } | null)?.message;
          return (
            <li key={ev.seq} className="text-text3 text-[12.5px] italic">
              · {message}
            </li>
          );
        }
        if (ev.type === "output_emit") {
          const e = ev.event as { kind?: string } | null;
          return (
            <li key={ev.seq} className="text-text2 text-[12.5px]">
              emitted output ({e?.kind ?? "unknown kind"})
            </li>
          );
        }
        if (ev.type === "action_request_emit") {
          const e = ev.event as
            | { kind?: string; risk_level?: string }
            | null;
          return (
            <li key={ev.seq} className="text-text2 text-[12.5px]">
              proposed action: {e?.kind ?? "unknown"}
              {e?.risk_level ? ` (risk ${e.risk_level})` : ""}
            </li>
          );
        }
        if (ev.type === "needs_input") {
          const e = ev.event as { question?: string } | null;
          return (
            <li key={ev.seq} className="text-warn-ink text-[12.5px]">
              paused for input: {e?.question}
            </li>
          );
        }
        if (ev.type === "error") {
          const e = ev.event as { message?: string } | null;
          return (
            <li key={ev.seq} className="text-danger text-[12.5px]">
              error: {e?.message}
            </li>
          );
        }
        if (ev.type === "done") {
          return (
            <li key={ev.seq} className="text-text3 text-[11.5px] font-mono">
              — done
            </li>
          );
        }
        return null;
      })}
    </ul>
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
    <section className="mb-7">
      <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-2.5">{title}</div>
      <div>{children}</div>
    </section>
  );
}
