"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

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

const PHASES = ["observe", "understand", "decide", "act"] as const;
type Phase = (typeof PHASES)[number];

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

type PhaseBucket = {
  phase: Phase | "setup";
  events: RunDetailPayload["events"];
  durationMs: number | null;
};

function bucketEventsByPhase(
  events: RunDetailPayload["events"],
): PhaseBucket[] {
  const buckets: Record<Phase | "setup", PhaseBucket> = {
    setup: { phase: "setup", events: [], durationMs: null },
    observe: { phase: "observe", events: [], durationMs: null },
    understand: { phase: "understand", events: [], durationMs: null },
    decide: { phase: "decide", events: [], durationMs: null },
    act: { phase: "act", events: [], durationMs: null },
  };

  let currentPhase: Phase | "setup" = "setup";
  let phaseStartSeq: Record<Phase, number | null> = {
    observe: null,
    understand: null,
    decide: null,
    act: null,
  };
  let phaseEndSeq: Record<Phase, number | null> = {
    observe: null,
    understand: null,
    decide: null,
    act: null,
  };

  for (const ev of events) {
    if (ev.type === "phase_start") {
      const phase = ((ev.event as { phase?: string } | null)?.phase ?? "")
        .toLowerCase() as Phase;
      if (PHASES.includes(phase)) {
        currentPhase = phase;
        phaseStartSeq[phase] = ev.seq;
        continue;
      }
    }
    if (ev.type === "phase_end") {
      const phase = ((ev.event as { phase?: string } | null)?.phase ?? "")
        .toLowerCase() as Phase;
      if (PHASES.includes(phase)) {
        phaseEndSeq[phase] = ev.seq;
        currentPhase = "setup";
        continue;
      }
    }
    buckets[currentPhase].events.push(ev);
  }

  // Phase duration proxy: number of events between start and end seq. Real
  // timestamps would need each event to carry one — the Run replay endpoint
  // currently doesn't return per-event timestamps. Event-count works as a
  // visual proxy for "how much happened in this phase."
  for (const p of PHASES) {
    const s = phaseStartSeq[p];
    const e = phaseEndSeq[p];
    if (s != null && e != null) {
      buckets[p].durationMs = Math.max(1, e - s);
    } else if (buckets[p].events.length > 0) {
      buckets[p].durationMs = buckets[p].events.length;
    }
  }

  return [
    buckets.setup,
    buckets.observe,
    buckets.understand,
    buckets.decide,
    buckets.act,
  ].filter((b) => b.events.length > 0 || b.durationMs != null);
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
    async (id: string, decision: "approve" | "reject") => {
      setActionBusyId(id);
      try {
        await fetch(`/api/action-requests/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ decision }),
        });
        await load();
      } finally {
        setActionBusyId(null);
      }
    },
    [load],
  );

  const phaseBuckets = useMemo(
    () => (data ? bucketEventsByPhase(data.events) : []),
    [data],
  );

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
        <div className="run-error">{error}</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>
        <div className="run-loading">Loading…</div>
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

        <div className="run-crumb">
          <button
            type="button"
            className="run-crumb-link"
            onClick={() => router.push("/workflows")}
          >
            ← Workflows
          </button>
        </div>

        <div className="run-header">
          <div className="run-header-row">
            <h1 className="run-title">{workflow?.name ?? "Run"}</h1>
            <button
              type="button"
              className="run-followup-btn"
              onClick={askFollowUp}
              title="Open an Ask thread pre-loaded with this run's context"
            >
              Ask a follow-up →
            </button>
          </div>
          <div className="run-header-meta">
            <span className="run-mono">{formatTime(run.startedAt ?? run.createdAt)}</span>
            <span className="run-sep">·</span>
            <span>{formatTrigger(run.triggerKind)}</span>
            <span className="run-sep">·</span>
            <span className={`run-status run-status-${run.status}`}>
              {run.status}
            </span>
            <span className="run-sep">·</span>
            <span className="run-mono">{formatDuration(durationMs)}</span>
          </div>
        </div>

        {/* Only render the phase strip when the agent emitted real phase
            markers (phase_start/phase_end). Showing a lone "SETUP" segment
            for runs that never tagged phases is noise; hide it entirely. */}
        {phaseBuckets.some((b) => b.phase !== "setup") && (
          <PhaseStrip buckets={phaseBuckets} />
        )}

        <Section title="Produced">
          {outputs.length === 0 ? (
            <p className="run-empty">
              This run hasn't produced any outputs yet.
            </p>
          ) : (
            <ul className="run-outputs">
              {outputs.map((o) => (
                <li key={o.id} className="run-output-card">
                  <div className="run-output-head">
                    <div className="run-output-title">{o.title}</div>
                    {o.mood && (
                      <span className={`run-output-mood run-output-mood-${o.mood}`}>
                        {o.mood}
                      </span>
                    )}
                  </div>
                  {o.body && <p className="run-output-body">{o.body}</p>}
                  <div className="run-output-foot">
                    <span className="run-output-kind">{o.kind}</span>
                    {o.scope && (
                      <>
                        <span className="run-sep">·</span>
                        <span className="run-output-scope">scope: {o.scope}</span>
                      </>
                    )}
                    <button
                      type="button"
                      className="run-output-pin"
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

        <Section title="Proposed">
          {actions.length === 0 ? (
            <p className="run-empty">No actions proposed by this run.</p>
          ) : (
            <ul className="run-actions">
              {actions.map((a) => (
                <li key={a.id} className="run-action-card">
                  <div className="run-action-head">
                    <div className="run-action-title">{a.summary || a.kind}</div>
                    <span
                      className={`run-action-pill run-action-pill-${a.status}`}
                    >
                      {a.status.replace(/_/g, " ")}
                    </span>
                  </div>
                  <div className="run-action-meta">
                    <span className="run-action-kind">{a.kind}</span>
                    {a.target && (
                      <>
                        <span className="run-sep">·</span>
                        <span className="run-action-target">{a.target}</span>
                      </>
                    )}
                    {a.riskLevel && (
                      <>
                        <span className="run-sep">·</span>
                        <span
                          className={`run-action-risk run-action-risk-${a.riskLevel}`}
                        >
                          {a.riskLevel}
                        </span>
                      </>
                    )}
                  </div>
                  {a.status === "pending_approval" && (
                    <div className="run-action-buttons">
                      <button
                        type="button"
                        className="run-action-btn is-primary"
                        disabled={actionBusyId === a.id}
                        onClick={() => void actOnRequest(a.id, "approve")}
                      >
                        Approve
                      </button>
                      <button
                        type="button"
                        className="run-action-btn"
                        disabled={actionBusyId === a.id}
                        onClick={() => void actOnRequest(a.id, "reject")}
                      >
                        Reject
                      </button>
                    </div>
                  )}
                  {a.status === "rejected" && a.rejectionReason && (
                    <p className="run-action-reason">
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
          <div className="run-expander-body">
            {phaseBuckets.length === 0 ? (
              <p className="run-empty">No events recorded.</p>
            ) : (
              phaseBuckets.map((bucket) => (
                <PhaseBlock key={bucket.phase} bucket={bucket} />
              ))
            )}
          </div>
        </details>

        <details
          className="run-expander"
          open={showLineage}
          onToggle={(e) => setShowLineage(e.currentTarget.open)}
        >
          <summary className="run-expander-summary">Lineage</summary>
          <div className="run-expander-body">
            {!lineage.upstream && run.triggerKind === "manual" && (
              <p className="run-empty">
                Started manually from the workflow drawer.
              </p>
            )}
            {!lineage.upstream && run.triggerKind === "cron" && (
              <p className="run-empty">
                Scheduled by cron · no upstream output.
              </p>
            )}
            {lineage.upstream && (
              <div className="run-lineage">
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
                  <span className="run-lineage-output">
                    &ldquo;{lineage.upstream.output.title}&rdquo;
                  </span>
                </p>
                {lineage.upstream.workflowRunId && (
                  <button
                    type="button"
                    className="run-lineage-link"
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

function PhaseStrip({ buckets }: { buckets: PhaseBucket[] }) {
  const total = buckets.reduce((s, b) => s + (b.durationMs ?? 1), 0);
  return (
    <div className="run-phase-strip">
      {buckets.map((b) => {
        const w = ((b.durationMs ?? 1) / total) * 100;
        return (
          <div
            key={b.phase}
            className={`run-phase-seg run-phase-seg-${b.phase}`}
            style={{ flexBasis: `${w}%` }}
            title={b.phase}
          >
            <span className="run-phase-seg-label">
              {b.phase === "setup" ? "setup" : b.phase}
            </span>
          </div>
        );
      })}
    </div>
  );
}

type RenderedItem =
  | { type: "message-block"; seq: number; content: string }
  | { type: "single"; seq: number; ev: PhaseBucket["events"][number] };

function coalesceEvents(events: PhaseBucket["events"]): RenderedItem[] {
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

function PhaseBlock({ bucket }: { bucket: PhaseBucket }) {
  const label =
    bucket.phase === "setup"
      ? "BEFORE PHASES"
      : bucket.phase.toUpperCase();
  const items = coalesceEvents(bucket.events);
  return (
    <div className="run-phase-block">
      <div className="run-phase-block-title">{label}</div>
      <ul className="run-phase-block-events">
        {items.map((item) => {
          if (item.type === "message-block") {
            return (
              <li key={item.seq} className="run-evt run-evt-message">
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
              <li key={ev.seq} className="run-evt run-evt-status">
                · {message}
              </li>
            );
          }
          if (ev.type === "output_emit") {
            const e = ev.event as { kind?: string } | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-info">
                emitted output ({e?.kind ?? "unknown kind"})
              </li>
            );
          }
          if (ev.type === "action_request_emit") {
            const e = ev.event as
              | { kind?: string; risk_level?: string }
              | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-info">
                proposed action: {e?.kind ?? "unknown"}
                {e?.risk_level ? ` (risk ${e.risk_level})` : ""}
              </li>
            );
          }
          if (ev.type === "observation_emit") {
            return (
              <li key={ev.seq} className="run-evt run-evt-info">
                wrote observation
              </li>
            );
          }
          if (ev.type === "decision_emit") {
            const e = ev.event as
              | { summary?: string; confidence?: number }
              | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-info">
                decided: {e?.summary ?? ""}
                {e?.confidence ? ` (conf ${e.confidence})` : ""}
              </li>
            );
          }
          if (ev.type === "understanding_note") {
            const e = ev.event as { note?: string } | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-message">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>
                  {e?.note ?? ""}
                </ReactMarkdown>
              </li>
            );
          }
          if (ev.type === "needs_input") {
            const e = ev.event as { question?: string } | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-warn">
                paused for input: {e?.question}
              </li>
            );
          }
          if (ev.type === "error") {
            const e = ev.event as { message?: string } | null;
            return (
              <li key={ev.seq} className="run-evt run-evt-error">
                error: {e?.message}
              </li>
            );
          }
          if (ev.type === "done") {
            return (
              <li key={ev.seq} className="run-evt run-evt-done">
                — done
              </li>
            );
          }
          return null;
        })}
      </ul>
    </div>
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
    <section className="run-section">
      <div className="run-section-title">{title}</div>
      <div className="run-section-body">{children}</div>
    </section>
  );
}
