"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Workflow, Trash2 } from "lucide-react";
import { cn } from "@/lib/cn";
import { confirmDialog } from "@/components/ConfirmModal";
import { describeSchedule } from "@/lib/cron-english";
import { formatSavedShort } from "@/lib/hours-saved";
import { Sparkline } from "@/components/Sparkline";

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
    minutesSaved30d: number;
    createdByThreadId: string | null;
    createdByRunId: string | null;
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

  const deleteWorkflow = useCallback(
    async (id: string, name: string) => {
      const ok = await confirmDialog({
        title: `Delete "${name}"?`,
        description:
          "This removes the workflow along with its triggers, run history, and proposed actions.",
        confirmLabel: "Delete",
        destructive: true,
      });
      if (!ok) return;
      const res = await fetch(`/api/workflows/${id}`, { method: "DELETE" });
      if (!res.ok) {
        setError(`Couldn't delete (HTTP ${res.status})`);
        return;
      }
      if (selectedId === id) select(null);
      setWorkflows((prev) => prev?.filter((w) => w.id !== id) ?? prev);
      void fetchList();
    },
    [selectedId, select, fetchList],
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

  const totalCount =
    grouped.active.length + grouped.paused.length + grouped.broken.length;

  return (
    <>
      <div className="flex items-center gap-3 mb-6">
        <div className="w-9 h-9 rounded-xl bg-accent-soft text-accent inline-flex items-center justify-center shrink-0">
          <Workflow size={16} strokeWidth={2} />
        </div>
        <div>
          <div className="font-display text-2xl font-bold leading-[1.1] text-text">Workflows</div>
          <div className="text-[13px] text-text3 mt-0.5">
            {workflows === null
              ? "Loading…"
              : totalCount === 0
                ? "None yet"
                : `${totalCount} ${totalCount === 1 ? "watcher" : "watchers"}`}
          </div>
        </div>
        <button
          type="button"
          className="workflows-new-btn library-head-action ml-auto"
          onClick={() =>
            router.push(
              `/work?seed=${encodeURIComponent("Set up a new workflow that ")}`,
            )
          }
        >
          + New workflow
        </button>
      </div>

      {error ? (
          <div className="py-10 text-center text-sm text-danger">{error}</div>
        ) : workflows === null ? (
          <div className="py-20 text-center text-[15px] text-text3">Loading…</div>
        ) : workflows.length === 0 ? (
          <div className="py-20 text-center text-[15px] text-text3">
            No workflows yet. <button
              type="button"
              className="bg-transparent border-0 text-accent font-inherit cursor-pointer underline underline-offset-[3px] p-0"
              onClick={() =>
                router.push(
                  `/work?seed=${encodeURIComponent("Set up a new workflow that ")}`,
                )
              }
            >+ New workflow</button> gets you started.
          </div>
        ) : (
          <div className="workflows-list min-w-0">
            {grouped.active.length > 0 && (
              <WorkflowGroup
                title="Active"
                count={grouped.active.length}
                items={grouped.active}
                selectedId={selectedId}
                onSelect={select}
                onDelete={deleteWorkflow}
                onMutated={fetchList}
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
                onDelete={deleteWorkflow}
                onMutated={fetchList}
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
                onDelete={deleteWorkflow}
                onMutated={fetchList}
                sparklines={sparklines}
                onSparkline={recordSparkline}
              />
            )}
          </div>
      )}
    </>
  );
}

function WorkflowGroup({
  title,
  count,
  items,
  selectedId,
  onSelect,
  onDelete,
  onMutated,
  sparklines,
  onSparkline,
}: {
  title: string;
  count: number;
  items: WorkflowListItem[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  onDelete: (id: string, name: string) => void;
  onMutated: () => void;
  sparklines: Record<string, number[]>;
  onSparkline: (id: string, values: number[]) => void;
}) {
  return (
    <section className="mb-7 last:mb-0">
      <div className="text-[11px] font-bold tracking-[0.12em] uppercase text-text3 mb-2.5">
        {title} <span className="text-text3 font-semibold tracking-[0.06em] ml-0.5">({count})</span>
      </div>
      <ul className="list-none flex flex-col gap-2 wf-grid">
        {items.map((w) => (
          <WorkflowRow
            key={w.id}
            w={w}
            active={selectedId === w.id}
            onSelect={() => onSelect(selectedId === w.id ? null : w.id)}
            onDelete={() => onDelete(w.id, w.name)}
            onMutated={onMutated}
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
  onDelete,
  onMutated,
  sparkline,
  onSparkline,
}: {
  w: WorkflowListItem;
  active: boolean;
  onSelect: () => void;
  onDelete: () => void;
  onMutated: () => void;
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
      <div className={`workflows-row${active ? " is-active" : ""}`}>
        <button
          type="button"
          className="workflows-row-main"
          onClick={onSelect}
          aria-expanded={active}
        >
          <div className="font-display text-base font-bold tracking-[-0.01em] text-text">{w.name}</div>
          {w.description && (
            <div className="text-[13px] text-text2 mt-1 leading-[1.45]">{w.description}</div>
          )}
          <div className="workflows-row-meta">
            <span>
              {describeSchedule(w.cron, w.cronTimezone, w.cronEnabled)}
            </span>
            {hasActivity && (
              <>
                <span className="opacity-60">·</span>
                <Sparkline values={sparkline ?? []} />
              </>
            )}
          </div>
        </button>
        <button
          type="button"
          className="workflows-row-delete"
          title="Delete workflow"
          aria-label={`Delete ${w.name}`}
          onClick={(event) => {
            event.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 size={14} strokeWidth={2} />
        </button>
        {active && <WorkflowDetail workflowId={w.id} onMutated={onMutated} />}
      </div>
    </li>
  );
}

function WorkflowDetail({
  workflowId,
  onMutated,
}: {
  workflowId: string;
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

  // OL7: park until next UTC midnight; the worker's cron sweep re-enables.
  const pauseForToday = useCallback(async () => {
    if (!data) return;
    setBusy(true);
    try {
      await fetch(`/api/workflows/${workflowId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pauseForToday: true }),
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
      <div className="workflow-detail">
        <div className="py-6 text-center text-[13px] text-danger">{error}</div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="workflow-detail">
        <div className="py-6 text-center text-[13px] text-text3">Loading…</div>
      </div>
    );
  }

  const { workflow, subscriptions, recentRuns, recentActions } = data;
  const budgetUsed = workflow.runsToday;
  const budgetCap = workflow.dailyRunBudget;
  const budgetPct = budgetCap ? Math.round((budgetUsed / budgetCap) * 100) : 0;

  return (
    <div className="workflow-detail">
      <div className="text-xs text-text3 mb-4 font-mono">
        {workflow.enabled ? "active" : "paused"}
        {workflow.cron ? ` · ${workflow.cronEnabled ? "cron" : "cron paused"}` : ""}
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
        {workflow.enabled && (
          <button
            type="button"
            className="workflow-drawer-btn"
            onClick={pauseForToday}
            disabled={busy}
            title="Pause until midnight UTC; resumes automatically"
          >
            Pause for today
          </button>
        )}
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

      {workflow.minutesSaved30d > 0 && (
        <Section title="Hours saved (30d)">
          <p className="leading-[1.55]">
            <span className="font-display font-bold text-accent">
              {formatSavedShort(workflow.minutesSaved30d)}
            </span>{" "}
            <span className="text-text2">
              of human time, estimated across this workflow&apos;s runs and actions.
            </span>
          </p>
        </Section>
      )}

      {workflow.description && (
        <Section title="Description">
          <p className="leading-[1.55]">{workflow.description}</p>
        </Section>
      )}

      {workflow.goal && (
        <Section title="Goal">
          <p className="leading-[1.55]">{workflow.goal}</p>
        </Section>
      )}

      {workflow.steps?.some((s) => s.description?.trim()) && (
        <Section title="Steps">
          <ol className="m-0 pl-5 [&>li]:mb-1 [&>li]:leading-[1.4]">
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
          <p className="text-text3">
            No subscriptions yet. Add one by editing this workflow.
          </p>
        ) : (
          <ul className="list-none p-0 m-0 flex flex-col gap-1.5">
            {subscriptions.map((s) => (
              <li key={s.id} className="flex items-baseline gap-2 leading-[1.45]">
                <span
                  className={cn(
                    "inline-block w-1.5 h-1.5 rounded-full flex-none -translate-y-px",
                    s.enabled ? "bg-success-mid" : "bg-text3",
                  )}
                  aria-hidden="true"
                />
                {describeSubscription(s)}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Schedule">
        <div className="flex items-center justify-between gap-3 text-[13px]">
          <span>
            {describeSchedule(
              workflow.cron,
              workflow.cronTimezone,
              workflow.cronEnabled,
            )}
          </span>
          {workflow.cron && (
            <label className="inline-flex items-center gap-1.5 text-xs text-text3 cursor-pointer">
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
        <div className="flex items-center justify-between gap-3 text-[13px]">
          {budgetCap == null ? (
            <span className="text-text3">No cap set</span>
          ) : (
            <>
              <span>
                {budgetUsed} / {budgetCap} runs used today
              </span>
              <span
                className={cn(
                  "font-mono text-xs",
                  budgetPct >= 80 ? "text-watch font-semibold" : "text-text2",
                )}
              >
                {budgetPct}%
              </span>
            </>
          )}
        </div>
      </Section>

      <Section title="Rules">
        {policies === null ? (
          <p className="text-text3">Loading…</p>
        ) : policies.length === 0 ? (
          <p className="text-text3">
            No rules set. Actions will require approval by default.
          </p>
        ) : (
          <ul className="list-none p-0 mt-0 mb-1.5 flex flex-col gap-1.5">
            {policies.map((p) => (
              <li key={p.id} className="flex items-center gap-2 text-[12.5px]">
                <span className="font-mono text-[11.5px] text-text2">{p.name}</span>
                <span
                  className={cn(
                    "text-[9.5px] font-bold tracking-[0.13em] uppercase px-1.5 py-0.5 rounded-full ml-auto",
                    policyModeClass(p.mode),
                  )}
                >
                  {describePolicyMode(p.mode)}
                </span>
              </li>
            ))}
          </ul>
        )}
        <a className="inline-block mt-1 text-xs text-accent no-underline hover:underline hover:underline-offset-2" href="/settings/rules">
          see all rules →
        </a>
      </Section>

      <Section title="Recent runs">
        {recentRuns.length === 0 ? (
          <p className="text-text3">No runs yet.</p>
        ) : (
          <ul className="list-none p-0 m-0 flex flex-col gap-1.5">
            {recentRuns.map((r) => (
              <li key={r.id} className="flex items-baseline gap-2 text-[13px]">
                <button
                  type="button"
                  className="workflow-drawer-run-link"
                  onClick={() => router.push(`/runs/${r.id}`)}
                >
                  <span
                    className={cn(
                      "inline-block w-3.5 text-center font-mono",
                      runStatusColor(r.status),
                    )}
                  >
                    {statusGlyph(r.status)}
                  </span>
                  <span className="workflow-drawer-run-meta font-mono text-xs text-text2">
                    {formatRelative(r.createdAt)} · {r.triggerKind} ·{" "}
                    {formatDuration(r.durationMs)}
                  </span>
                  <span className="workflow-drawer-run-arrow ml-auto text-text3 font-mono text-[11.5px] transition-[color,transform] duration-[0.18s]" aria-hidden="true">
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
          <p className="text-text3">No actions proposed yet.</p>
        ) : (
          <ul className="list-none p-0 m-0 flex flex-col gap-1.5">
            {recentActions.map((a) => (
              <li key={a.id} className="border border-border bg-neutral-soft rounded-lg px-2.5 py-2">
                <div className="flex items-center gap-2 flex-wrap text-[12.5px]">
                  <span className="font-semibold text-text">{a.kind}</span>
                  {a.target && (
                    <span className="font-mono text-[11.5px] text-text2">
                      {a.target}
                    </span>
                  )}
                  <span
                    className={cn(
                      "ml-auto text-[10.5px] font-bold tracking-[0.08em] uppercase px-2 py-0.5 rounded-full",
                      actionPillClass(a.status),
                    )}
                  >
                    {actionStatusLabel(a.status)}
                  </span>
                </div>
                <div className="mt-1 text-[11.5px] text-text3">
                  {formatRelative(a.createdAt)}
                  {a.summary ? ` · ${a.summary}` : ""}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <div className="mt-6 pt-3 border-t border-border flex items-center justify-between gap-3">
        {workflow.createdByThreadId ? (
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer text-xs font-semibold py-1 px-0 hover:text-accent hover:underline hover:underline-offset-[3px]"
            onClick={() => router.push(`/work/${workflow.createdByThreadId}`)}
          >
            view conversation
          </button>
        ) : (
          <span />
        )}
        <button
          type="button"
          className="bg-transparent border-0 text-accent cursor-pointer text-xs font-semibold py-1 px-0 hover:underline hover:underline-offset-[3px]"
          onClick={() =>
            router.push(
              `/work?seed=${encodeURIComponent(
                `Update the '${workflow.name}' workflow to `,
              )}`,
            )
          }
        >
          edit in /work
        </button>
      </div>
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
    <div className="mb-[18px]">
      <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-1.5">{title}</div>
      <div className="text-[13.5px] text-text leading-[1.5]">{children}</div>
    </div>
  );
}

function policyModeClass(mode: string): string {
  switch (mode) {
    case "auto_approve":
      return "bg-success-soft text-success-mid";
    case "approval_required":
      return "bg-watch-soft text-warn-ink";
    case "never":
      return "bg-danger-soft text-danger";
    default:
      return "bg-neutral text-text2";
  }
}

function runStatusColor(status: string): string {
  switch (status) {
    case "completed":
      return "text-success-mid";
    case "failed":
      return "text-danger";
    case "needs_input":
    case "waiting_approval":
      return "text-watch";
    default:
      return "text-text3";
  }
}

function actionPillClass(status: string): string {
  switch (status) {
    case "executed":
      return "bg-success-soft text-success-mid";
    case "pending_approval":
      return "bg-watch-soft text-warn-ink";
    case "rejected":
    case "failed":
      return "bg-danger-soft text-danger";
    default:
      return "bg-neutral text-text2";
  }
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
