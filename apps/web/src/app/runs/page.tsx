"use client";

import { Suspense, useCallback, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { Button } from "@/components/ui/Button";
import { Pill, type PillVariant } from "@/components/ui/Pill";
import { Segment, SegmentedControl } from "@/components/ui/Tabs";

type StatusFilter = "active" | "completed" | "failed" | "all";

type WorkflowRunRow = {
  id: string;
  workflow: { id: string; name: string };
  triggerKind: string;
  executionMode: "single" | "batch" | null;
  chainDepth: number;
  status: string;
  summary: string | null;
  error: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  durationMs: number | null;
  createdAt: string;
  updatedAt: string;
  outputCount: number;
  actionCount: number;
  pendingActionCount: number;
};

type RunsPayload = {
  runs: WorkflowRunRow[];
  status: StatusFilter;
};

const TABS: Array<{ key: StatusFilter; label: string }> = [
  { key: "all", label: "All" },
  { key: "active", label: "Active" },
  { key: "completed", label: "Completed" },
  { key: "failed", label: "Failed" },
];

function isStatusFilter(value: string | null): value is StatusFilter {
  return (
    value === "active" ||
    value === "completed" ||
    value === "failed" ||
    value === "all"
  );
}

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const min = Math.max(0, Math.round(ms / 60000));
  if (min < 1) return "just now";
  if (min < 60) return `${min}m ago`;
  const hours = Math.round(min / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

function formatDuration(ms: number | null): string {
  if (ms == null) return "running";
  if (ms < 1000) return `${ms}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

function statusLabel(status: string): string {
  return status.replace(/_/g, " ");
}

function statusVariant(status: string): PillVariant {
  switch (status) {
    case "completed":
      return "success";
    case "failed":
    case "cancelled":
      return "danger";
    case "queued":
    case "running":
    case "needs_input":
    case "waiting_approval":
      return "watch";
    default:
      return "muted";
  }
}

function describeRun(run: WorkflowRunRow): string {
  if (run.error) return run.error;
  if (run.summary?.trim()) return run.summary;
  if (run.outputCount > 0 || run.actionCount > 0) {
    const parts = [];
    if (run.outputCount > 0) {
      parts.push(
        `${run.outputCount} ${run.outputCount === 1 ? "finding" : "findings"}`,
      );
    }
    if (run.actionCount > 0) {
      parts.push(
        `${run.actionCount} ${run.actionCount === 1 ? "action" : "actions"}`,
      );
    }
    return parts.join(" · ");
  }
  if (run.status === "completed") return "Looked at the data; nothing to flag.";
  return "Run is still collecting context.";
}

export default function RunsPage() {
  return (
    <Suspense fallback={null}>
      <RunsPageInner />
    </Suspense>
  );
}

function RunsPageInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const initial = searchParams?.get("status");
  const [filter, setFilter] = useState<StatusFilter>(
    isStatusFilter(initial) ? initial : "all",
  );
  const [data, setData] = useState<RunsPayload | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/workflow-runs?status=${filter}`, {
        cache: "no-store",
      });
      if (!res.ok) {
        setError(`Couldn't load runs (HTTP ${res.status})`);
        return;
      }
      setData((await res.json()) as RunsPayload);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load runs");
    }
  }, [filter]);

  useEffect(() => {
    const initialLoadId = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(initialLoadId);
  }, [load]);

  useEffect(() => {
    const active = data?.runs.some((r) =>
      ["queued", "running", "needs_input", "waiting_approval"].includes(
        r.status,
      ),
    );
    if (!active) return;
    const id = setInterval(() => void load(), 3000);
    return () => clearInterval(id);
  }, [data?.runs, load]);

  const switchFilter = useCallback((next: StatusFilter) => {
    setFilter(next);
    const url = new URL(window.location.href);
    if (next === "all") url.searchParams.delete("status");
    else url.searchParams.set("status", next);
    window.history.replaceState({}, "", url.toString());
  }, []);

  return (
    <>
      <div className="root">
        <AppHeader back={{ href: "/workflows", label: "Agent workflows" }}>
          <SectionNav current="workflows" />
        </AppHeader>

        <PageHeading
          title="Run history"
          description="Inspect recent workflow runs, findings, and proposed actions."
          meta={data ? `${data.runs.length} shown` : undefined}
        />

        <SegmentedControl aria-label="Run status filter" className="mb-5">
          {TABS.map((tab) => (
            <Segment
              key={tab.key}
              selected={filter === tab.key}
              onClick={() => switchFilter(tab.key)}
            >
              {tab.label}
            </Segment>
          ))}
        </SegmentedControl>

        {error ? (
          <div className="py-[50px] text-center text-sm text-danger">
            {error}
          </div>
        ) : !data ? (
          <div className="py-[50px] text-center text-sm text-text3">
            Loading…
          </div>
        ) : data.runs.length === 0 ? (
          <div className="py-[50px] text-center text-sm text-text3">
            No workflow runs yet.
          </div>
        ) : (
          <ul className="list-none p-0 m-0 flex flex-col gap-2.5">
            {data.runs.map((run) => (
              <li key={run.id}>
                <Button
                  variant="secondary"
                  size="md"
                  className="!min-h-0 w-full !items-stretch !justify-start !whitespace-normal !rounded-2xl px-4 py-3.5 text-left hover:shadow-soft"
                  onClick={() => router.push(`/runs/${run.id}`)}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-semibold text-text">
                          {run.workflow.name}
                        </span>
                        <span className="font-mono text-ui-caption text-text3">
                          {run.triggerKind}
                          {run.chainDepth > 0
                            ? ` · chain ${run.chainDepth}`
                            : ""}
                        </span>
                        {run.triggerKind === "api" && run.executionMode ? (
                          <Pill
                            variant={
                              run.executionMode === "batch" ? "success" : "muted"
                            }
                          >
                            {run.executionMode}
                          </Pill>
                        ) : null}
                      </div>
                      <p className="mt-1.5 mb-0 text-ui-body-sm leading-[1.45] text-text2 line-clamp-2">
                        {describeRun(run)}
                      </p>
                    </div>
                    <Pill variant={statusVariant(run.status)}>
                      {statusLabel(run.status)}
                    </Pill>
                  </div>
                  <div className="mt-3 flex flex-wrap items-center gap-1.5 text-ui-caption text-text3">
                    <span className="font-mono">
                      {formatRelative(run.createdAt)}
                    </span>
                    <span className="text-text3/70">·</span>
                    <span className="font-mono">
                      {formatDuration(run.durationMs)}
                    </span>
                    <span className="text-text3/70">·</span>
                    <span>
                      {run.outputCount}{" "}
                      {run.outputCount === 1 ? "finding" : "findings"}
                    </span>
                    <span className="text-text3/70">·</span>
                    <span>
                      {run.actionCount}{" "}
                      {run.actionCount === 1 ? "action" : "actions"}
                    </span>
                    {run.pendingActionCount > 0 && (
                      <>
                        <span className="text-text3/70">·</span>
                        <span className="text-warn-ink font-semibold">
                          {run.pendingActionCount} awaiting you
                        </span>
                      </>
                    )}
                    <span className="ml-auto font-mono text-ui-caption text-text3">
                      →
                    </span>
                  </div>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <CreatorCredit />
    </>
  );
}
