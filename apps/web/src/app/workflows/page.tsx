"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { describeSchedule } from "@/lib/cron-english";

type WorkflowListItem = {
  id: string;
  name: string;
  description: string;
  goal: string;
  enabled: boolean;
  status: string;
  cron: string | null;
  cronTimezone: string;
  cronEnabled: boolean;
  steps: { id: string; description: string }[];
  createdAt: string;
  updatedAt: string;
};

type Subscription = {
  id: string;
  sourceKind: "workflow_output" | "source_change" | "external_event";
  filter: Record<string, unknown>;
  enabled: boolean;
  debounceMs: number;
};

type RecentRun = {
  id: string;
  status: string;
  triggerKind: string;
  summary: string | null;
  error: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  durationMs: number | null;
  createdAt: string;
};

type RecentAction = {
  id: string;
  workflowRunId: string;
  kind: string;
  target: string | null;
  status: string;
  riskLevel: string | null;
  summary: string;
  approvedAt: string | null;
  createdAt: string;
};

type DrawerPayload = {
  workflow: WorkflowListItem & {
    systemPromptOverlay: string;
    dailyRunBudget: number | null;
    runsToday: number;
  };
  subscriptions: Subscription[];
  recentRuns: RecentRun[];
  recentActions: RecentAction[];
  activitySparkline: number[];
};

type PolicySummary = {
  id: string;
  name: string;
  mode: string;
  riskThresholdAutoApprove: string | null;
};

function formatDuration(ms: number | null): string {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

function formatRelative(iso: string | null): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  const days = Math.floor(diff / 86_400_000);
  if (days < 14) return `${days}d ago`;
  return new Date(iso).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
  });
}

function describeSubscription(s: Subscription): string {
  if (s.sourceKind === "workflow_output") {
    const f = s.filter as {
      scope?: string;
      mood?: string[] | string;
      topic?: string;
    };
    const parts: string[] = [];
    if (f.scope) parts.push(`scoped '${f.scope}'`);
    if (f.topic) parts.push(`topic '${f.topic}'`);
    if (f.mood) {
      const moods = Array.isArray(f.mood) ? f.mood : [f.mood];
      parts.push(`mood ${moods.join(" or ")}`);
    }
    const tail = parts.length ? ` (${parts.join(", ")})` : "";
    return `Outputs from any workflow${tail}.`;
  }
  if (s.sourceKind === "source_change") {
    const f = s.filter as { table?: string; operation?: string };
    const tbl = f.table ?? "any table";
    const op = f.operation ? ` ${f.operation}s` : " changes";
    return `The ${tbl} table — watches for${op}.`;
  }
  const f = s.filter as { provider?: string; topic?: string };
  return `External events${f.provider ? ` from ${f.provider}` : ""}${
    f.topic ? ` on '${f.topic}'` : ""
  }.`;
}

function Sparkline({
  values,
  width = 88,
  height = 16,
}: {
  values: number[];
  width?: number;
  height?: number;
}) {
  if (values.length === 0) return null;
  const max = Math.max(1, ...values);
  const barW = width / values.length;
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden="true"
      style={{ display: "block" }}
    >
      {values.map((v, i) => {
        const h = (v / max) * height;
        return (
          <rect
            key={i}
            x={i * barW + 0.5}
            y={height - h}
            width={Math.max(1, barW - 1)}
            height={h}
            fill="var(--text3)"
            opacity={v === 0 ? 0.35 : 1}
          />
        );
      })}
    </svg>
  );
}

export default function WorkflowsPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const selectedId = searchParams?.get("id") ?? null;

  const [workflows, setWorkflows] = useState<WorkflowListItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [sparklines, setSparklines] = useState<Record<string, number[]>>({});

  const fetchList = useCallback(async () => {
    try {
      const res = await fetch("/api/workflows", { cache: "no-store" });
      const data = await res.json();
      setWorkflows(data.workflows ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load workflows");
    }
  }, []);

  useEffect(() => {
    void fetchList();
  }, [fetchList]);

  const select = useCallback(
    (id: string | null) => {
      const params = new URLSearchParams(searchParams?.toString() ?? "");
      if (id) params.set("id", id);
      else params.delete("id");
      const qs = params.toString();
      router.replace(qs ? `/workflows?${qs}` : "/workflows", { scroll: false });
    },
    [router, searchParams],
  );

  const grouped = useMemo(() => {
    const active: WorkflowListItem[] = [];
    const paused: WorkflowListItem[] = [];
    const broken: WorkflowListItem[] = [];
    for (const w of workflows ?? []) {
      if (w.status === "broken") broken.push(w);
      else if (!w.enabled) paused.push(w);
      else active.push(w);
    }
    return { active, paused, broken };
  }, [workflows]);

  const recordSparkline = useCallback((id: string, values: number[]) => {
    setSparklines((prev) =>
      prev[id]?.length === values.length &&
      prev[id]?.every((v, i) => v === values[i])
        ? prev
        : { ...prev, [id]: values },
    );
  }, []);

  return (
    <>
      <div className="root workflows-root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>

        <div className="workflows-head">
          <h1 className="workflows-title">Workflows</h1>
          <button
            type="button"
            className="workflows-new-btn"
            onClick={() => router.push("/workflows/new")}
          >
            + New workflow
          </button>
        </div>

        {error ? (
          <div className="workflows-error">{error}</div>
        ) : workflows === null ? (
          <div className="workflows-empty">Loading…</div>
        ) : workflows.length === 0 ? (
          <div className="workflows-empty">
            No workflows yet. <button
              type="button"
              className="workflows-empty-link"
              onClick={() => router.push("/workflows/new")}
            >+ New workflow</button> gets you started.
          </div>
        ) : (
          <div className="workflows-layout">
            <div className="workflows-list">
              {grouped.active.length > 0 && (
                <WorkflowGroup
                  title="Active"
                  count={grouped.active.length}
                  items={grouped.active}
                  selectedId={selectedId}
                  onSelect={select}
                  sparklines={sparklines}
                  onSparkline={recordSparkline}
                />
              )}
              {grouped.paused.length > 0 && (
                <WorkflowGroup
                  title="Paused"
                  count={grouped.paused.length}
                  items={grouped.paused}
                  selectedId={selectedId}
                  onSelect={select}
                  sparklines={sparklines}
                  onSparkline={recordSparkline}
                />
              )}
              {grouped.broken.length > 0 && (
                <WorkflowGroup
                  title="Needs attention"
                  count={grouped.broken.length}
                  items={grouped.broken}
                  selectedId={selectedId}
                  onSelect={select}
                  sparklines={sparklines}
                  onSparkline={recordSparkline}
                />
              )}
            </div>

            {selectedId && (
              <WorkflowDrawer
                key={selectedId}
                workflowId={selectedId}
                onClose={() => select(null)}
                onMutated={() => {
                  void fetchList();
                }}
              />
            )}
          </div>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function WorkflowGroup({
  title,
  count,
  items,
  selectedId,
  onSelect,
  sparklines,
  onSparkline,
}: {
  title: string;
  count: number;
  items: WorkflowListItem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  sparklines: Record<string, number[]>;
  onSparkline: (id: string, values: number[]) => void;
}) {
  return (
    <section className="workflows-group">
      <div className="workflows-group-title">
        {title} <span className="workflows-group-count">({count})</span>
      </div>
      <ul className="workflows-rows">
        {items.map((w) => (
          <WorkflowRow
            key={w.id}
            w={w}
            active={selectedId === w.id}
            onSelect={() => onSelect(w.id)}
            sparkline={sparklines[w.id]}
            onSparkline={(values) => onSparkline(w.id, values)}
          />
        ))}
      </ul>
    </section>
  );
}

function WorkflowRow({
  w,
  active,
  onSelect,
  sparkline,
  onSparkline,
}: {
  w: WorkflowListItem;
  active: boolean;
  onSelect: () => void;
  sparkline: number[] | undefined;
  onSparkline: (values: number[]) => void;
}) {
  // Lazy-load the per-workflow sparkline once when first rendered.
  useEffect(() => {
    if (sparkline) return;
    let cancelled = false;
    void fetch(`/api/workflows/${w.id}`, { cache: "no-store" })
      .then((r) => r.json())
      .then((data: DrawerPayload) => {
        if (cancelled) return;
        if (Array.isArray(data?.activitySparkline)) {
          onSparkline(data.activitySparkline);
        }
      })
      .catch(() => {
        if (cancelled) return;
        onSparkline([]);
      });
    return () => {
      cancelled = true;
    };
  }, [w.id, sparkline, onSparkline]);

  const hasActivity = sparkline?.some((v) => v > 0) ?? false;

  return (
    <li>
      <button
        type="button"
        className={`workflows-row${active ? " is-active" : ""}`}
        onClick={onSelect}
      >
        <div className="workflows-row-name">{w.name}</div>
        {w.description && (
          <div className="workflows-row-desc">{w.description}</div>
        )}
        <div className="workflows-row-meta">
          <span className="workflows-row-schedule">
            {describeSchedule(w.cron, w.cronTimezone, w.cronEnabled)}
          </span>
          {hasActivity && (
            <>
              <span className="workflows-row-sep">·</span>
              <Sparkline values={sparkline ?? []} />
            </>
          )}
        </div>
      </button>
    </li>
  );
}

function WorkflowDrawer({
  workflowId,
  onClose,
  onMutated,
}: {
  workflowId: string;
  onClose: () => void;
  onMutated: () => void;
}) {
  const router = useRouter();
  const [data, setData] = useState<DrawerPayload | null>(null);
  const [policies, setPolicies] = useState<PolicySummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/workflows/${workflowId}`, {
        cache: "no-store",
      });
      if (!res.ok) {
        setError(`Couldn't load (HTTP ${res.status})`);
        return;
      }
      const json = (await res.json()) as DrawerPayload;
      setData(json);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load");
    }
  }, [workflowId]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    // Policies are org-level today (no per-workflow override). Fetch the
    // active set so the drawer's Policy section reflects what's actually
    // gating actions for this org.
    void fetch("/api/policies", { cache: "no-store" })
      .then((r) => r.json())
      .then((d: { policies: PolicySummary[] }) =>
        setPolicies((d.policies ?? []).filter((p) => (p as unknown as { enabled?: boolean }).enabled !== false)),
      )
      .catch(() => setPolicies([]));
  }, []);

  const togglePause = useCallback(async () => {
    if (!data) return;
    setBusy(true);
    try {
      await fetch(`/api/workflows/${workflowId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: !data.workflow.enabled }),
      });
      await load();
      onMutated();
    } finally {
      setBusy(false);
    }
  }, [data, workflowId, load, onMutated]);

  const toggleCron = useCallback(async () => {
    if (!data) return;
    setBusy(true);
    try {
      await fetch(`/api/workflows/${workflowId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cronEnabled: !data.workflow.cronEnabled }),
      });
      await load();
      onMutated();
    } finally {
      setBusy(false);
    }
  }, [data, workflowId, load, onMutated]);

  const runNow = useCallback(async () => {
    setBusy(true);
    try {
      const res = await fetch(`/api/workflows/${workflowId}/runs`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({}),
      });
      if (!res.ok) {
        setError(`Couldn't run (HTTP ${res.status})`);
        return;
      }
      const json = await res.json();
      if (json.workflowRunId) {
        router.push(`/runs/${json.workflowRunId}`);
      } else if (json.threadId) {
        router.push(`/work/${json.threadId}`);
      } else {
        await load();
      }
    } finally {
      setBusy(false);
    }
  }, [workflowId, router, load]);

  if (error) {
    return (
      <aside className="workflow-drawer">
        <div className="workflow-drawer-head">
          <button
            type="button"
            className="workflow-drawer-close"
            onClick={onClose}
            aria-label="Close"
          >
            ×
          </button>
        </div>
        <div className="workflow-drawer-error">{error}</div>
      </aside>
    );
  }

  if (!data) {
    return (
      <aside className="workflow-drawer">
        <div className="workflow-drawer-head">
          <button
            type="button"
            className="workflow-drawer-close"
            onClick={onClose}
            aria-label="Close"
          >
            ×
          </button>
        </div>
        <div className="workflow-drawer-loading">Loading…</div>
      </aside>
    );
  }

  const { workflow, subscriptions, recentRuns, recentActions } = data;
  const budgetUsed = workflow.runsToday;
  const budgetCap = workflow.dailyRunBudget;
  const budgetPct = budgetCap ? Math.round((budgetUsed / budgetCap) * 100) : 0;

  return (
    <aside className="workflow-drawer">
      <div className="workflow-drawer-head">
        <div>
          <div className="workflow-drawer-name">{workflow.name}</div>
          <div className="workflow-drawer-status">
            {workflow.enabled ? "active" : "paused"}
            {workflow.cron ? ` · ${workflow.cronEnabled ? "cron" : "cron paused"}` : ""}
          </div>
        </div>
        <button
          type="button"
          className="workflow-drawer-close"
          onClick={onClose}
          aria-label="Close"
        >
          ×
        </button>
      </div>

      <div className="workflow-drawer-actions">
        <button
          type="button"
          className="workflow-drawer-btn"
          onClick={togglePause}
          disabled={busy}
        >
          {workflow.enabled ? "Pause" : "Resume"}
        </button>
        <button
          type="button"
          className="workflow-drawer-btn is-primary"
          onClick={runNow}
          disabled={busy || !workflow.enabled}
          title={
            !workflow.enabled
              ? "Resume the workflow first"
              : "Manually run this workflow now"
          }
        >
          + Run now
        </button>
      </div>

      {workflow.description && (
        <Section title="Description">
          <p className="workflow-drawer-prose">{workflow.description}</p>
        </Section>
      )}

      {workflow.goal && (
        <Section title="Goal">
          <p className="workflow-drawer-prose">{workflow.goal}</p>
        </Section>
      )}

      {workflow.steps?.some((s) => s.description?.trim()) && (
        <Section title="Steps">
          <ol className="workflow-drawer-steps">
            {workflow.steps
              .filter((s) => s.description?.trim())
              .map((step, i) => (
                <li key={step.id ?? i}>{step.description}</li>
              ))}
          </ol>
        </Section>
      )}

      <Section title="Watches">
        {subscriptions.length === 0 ? (
          <p className="workflow-drawer-muted">
            No subscriptions yet. Add one by editing this workflow.
          </p>
        ) : (
          <ul className="workflow-drawer-subs">
            {subscriptions.map((s) => (
              <li key={s.id}>
                <span
                  className={`workflow-drawer-sub-dot${s.enabled ? " on" : ""}`}
                  aria-hidden="true"
                />
                {describeSubscription(s)}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Schedule">
        <div className="workflow-drawer-row">
          <span>
            {describeSchedule(
              workflow.cron,
              workflow.cronTimezone,
              workflow.cronEnabled,
            )}
          </span>
          {workflow.cron && (
            <label className="workflow-drawer-toggle">
              <input
                type="checkbox"
                checked={workflow.cronEnabled}
                onChange={toggleCron}
                disabled={busy}
              />
              <span>{workflow.cronEnabled ? "enabled" : "disabled"}</span>
            </label>
          )}
        </div>
      </Section>

      <Section title="Daily budget">
        <div className="workflow-drawer-row">
          {budgetCap == null ? (
            <span className="workflow-drawer-muted">No cap set</span>
          ) : (
            <>
              <span>
                {budgetUsed} / {budgetCap} runs used today
              </span>
              <span
                className={`workflow-drawer-budget-pct${budgetPct >= 80 ? " warn" : ""}`}
              >
                {budgetPct}%
              </span>
            </>
          )}
        </div>
      </Section>

      <Section title="Policy">
        {policies === null ? (
          <p className="workflow-drawer-muted">Loading…</p>
        ) : policies.length === 0 ? (
          <p className="workflow-drawer-muted">
            No policies set. Actions will require approval by default.
          </p>
        ) : (
          <ul className="workflow-drawer-policies">
            {policies.map((p) => (
              <li key={p.id}>
                <span className="workflow-drawer-policy-name">{p.name}</span>
                <span
                  className={`workflow-drawer-policy-mode workflow-drawer-policy-mode-${p.mode}`}
                >
                  {describePolicyMode(p.mode)}
                </span>
              </li>
            ))}
          </ul>
        )}
        <a className="workflow-drawer-link" href="/settings/policies">
          see all policies →
        </a>
      </Section>

      <Section title="Recent runs">
        {recentRuns.length === 0 ? (
          <p className="workflow-drawer-muted">No runs yet.</p>
        ) : (
          <ul className="workflow-drawer-runs">
            {recentRuns.map((r) => (
              <li key={r.id}>
                <button
                  type="button"
                  className="workflow-drawer-run-link"
                  onClick={() => router.push(`/runs/${r.id}`)}
                >
                  <span
                    className={`workflow-drawer-run-status workflow-drawer-run-status-${r.status}`}
                  >
                    {statusGlyph(r.status)}
                  </span>
                  <span className="workflow-drawer-run-meta">
                    {formatRelative(r.createdAt)} · {r.triggerKind} ·{" "}
                    {formatDuration(r.durationMs)}
                  </span>
                  <span className="workflow-drawer-run-arrow" aria-hidden="true">
                    →
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Recent actions">
        {recentActions.length === 0 ? (
          <p className="workflow-drawer-muted">No actions proposed yet.</p>
        ) : (
          <ul className="workflow-drawer-actions-list">
            {recentActions.map((a) => (
              <li key={a.id}>
                <div className="workflow-drawer-action-line">
                  <span className="workflow-drawer-action-kind">{a.kind}</span>
                  {a.target && (
                    <span className="workflow-drawer-action-target">
                      {a.target}
                    </span>
                  )}
                  <span
                    className={`workflow-drawer-action-pill workflow-drawer-action-pill-${a.status}`}
                  >
                    {actionStatusLabel(a.status)}
                  </span>
                </div>
                <div className="workflow-drawer-action-meta">
                  {formatRelative(a.createdAt)}
                  {a.summary ? ` · ${a.summary}` : ""}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <div className="workflow-drawer-foot">
        <button
          type="button"
          className="workflow-drawer-edit"
          onClick={() => router.push(`/workflows/${workflowId}/edit`)}
        >
          edit this workflow
        </button>
      </div>
    </aside>
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
    <div className="workflow-drawer-section">
      <div className="workflow-drawer-section-title">{title}</div>
      <div className="workflow-drawer-section-body">{children}</div>
    </div>
  );
}

function statusGlyph(status: string): string {
  switch (status) {
    case "completed":
      return "✓";
    case "running":
    case "queued":
      return "…";
    case "failed":
      return "✗";
    case "cancelled":
      return "—";
    case "needs_input":
    case "waiting_approval":
      return "?";
    default:
      return "·";
  }
}

function describePolicyMode(mode: string): string {
  switch (mode) {
    case "auto_approve":
      return "auto-approves";
    case "approval_required":
      return "requires approval";
    case "observe_only":
      return "observes only";
    case "draft_only":
      return "drafts only";
    case "never":
      return "never executes";
    default:
      return mode.replace(/_/g, " ");
  }
}

function actionStatusLabel(status: string): string {
  switch (status) {
    case "pending_approval":
      return "pending";
    case "executed":
      return "executed";
    case "rejected":
      return "rejected";
    case "failed":
      return "failed";
    case "approved":
      return "approved";
    case "expired":
      return "expired";
    default:
      return status;
  }
}
