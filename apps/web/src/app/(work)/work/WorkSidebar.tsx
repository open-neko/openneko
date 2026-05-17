"use client";

import { Brain, Plus, Sparkles, Trash2, Workflow } from "lucide-react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { confirmDialog } from "@/components/ConfirmModal";
import { useWorkShell } from "../work-shell-context";

type ThreadSummary = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  lastMessageAt: string;
};

export default function WorkSidebar() {
  const router = useRouter();
  const params = useParams();
  const { activeRunId } = useWorkShell();
  const activeThreadId =
    typeof params?.threadId === "string" ? params.threadId : null;

  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [loadingThreads, setLoadingThreads] = useState(true);
  const [pendingCount, setPendingCount] = useState(0);

  const refresh = useCallback(async () => {
    const res = await fetch("/api/work/threads", { cache: "no-store" });
    if (!res.ok) return;
    const data = (await res.json()) as { threads: ThreadSummary[] };
    setThreads(data.threads ?? []);
    setLoadingThreads(false);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/work/memories/pending", { cache: "no-store" })
      .then((r) => (r.ok ? r.json() : { pending: [] }))
      .then((data: { pending?: unknown[] }) => {
        if (!cancelled) setPendingCount((data.pending ?? []).length);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  // Refresh the thread list when an active run finishes — title may have
  // been generated, lastMessageAt has shifted. We don't refresh on every nav
  // anymore (the sidebar persists across route changes).
  useEffect(() => {
    if (activeRunId) return;
    void refresh();
  }, [activeRunId, refresh]);

  async function createThread() {
    const res = await fetch("/api/work/threads", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    if (!res.ok) return;
    const data = (await res.json()) as { thread: ThreadSummary };
    setThreads((prev) => [data.thread, ...prev]);
    router.push(`/work/${data.thread.id}`);
  }

  async function deleteThread(threadId: string) {
    const target = threads.find((t) => t.id === threadId);
    const ok = await confirmDialog({
      title: `Delete "${target?.title || "Untitled thread"}"?`,
      description: "This also removes its run history.",
      confirmLabel: "Delete",
      destructive: true,
    });
    if (!ok) return;
    const res = await fetch(`/api/work/threads/${threadId}`, { method: "DELETE" });
    if (!res.ok) return;
    const remaining = threads.filter((t) => t.id !== threadId);
    setThreads(remaining);
    if (activeThreadId === threadId) {
      router.replace(remaining[0]?.id ? `/work/${remaining[0].id}` : "/work");
    }
  }

  return (
    <aside className="work-sidebar">
      <div className="work-sidebar-head">
        <div className="work-sidebar-eyebrow">
          <span>Threads</span>
          <span className="work-sidebar-count">{threads.length}</span>
        </div>
        <button
          className="work-icon-btn is-ghost"
          onClick={() => void createThread()}
          title="New thread"
          aria-label="New thread"
        >
          <Plus size={15} strokeWidth={2} />
        </button>
      </div>

      <div className="work-thread-list">
        {loadingThreads ? (
          <div className="text-text3 text-[13px] py-3 px-1">Loading threads…</div>
        ) : threads.length === 0 ? (
          <div className="text-text3 text-[13px] py-3 px-1">Start a thread to use Work.</div>
        ) : (
          threads.map((thread) => (
            <ThreadRow
              key={thread.id}
              thread={thread}
              active={thread.id === activeThreadId}
              running={thread.id === activeThreadId && Boolean(activeRunId)}
              onDelete={() => void deleteThread(thread.id)}
            />
          ))
        )}
      </div>

      <div className="work-sidebar-footer">
        <Link href="/workflows" className="work-sidebar-link">
          <Workflow size={14} strokeWidth={2} />
          <span>Workflows</span>
        </Link>
        <Link href="/skills" className="work-sidebar-link">
          <Sparkles size={14} strokeWidth={2} />
          <span>Skills</span>
        </Link>
        <Link href="/memory" className="work-sidebar-link">
          <Brain size={14} strokeWidth={2} />
          <span>Memory</span>
          {pendingCount > 0 ? (
            <span className="work-sidebar-badge">{pendingCount}</span>
          ) : null}
        </Link>
      </div>
    </aside>
  );
}

function formatDate(value: string): string {
  const d = new Date(value);
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

function ThreadRow({
  thread,
  active,
  running,
  onDelete,
}: {
  thread: ThreadSummary;
  active: boolean;
  running: boolean;
  onDelete: () => void;
}) {
  return (
    <div className={`work-thread-row${active ? " is-active" : ""}`}>
      <Link
        href={`/work/${thread.id}`}
        className="work-thread-row-main"
        title={thread.title || "Untitled thread"}
        prefetch={false}
      >
        <span
          className={`work-thread-dot${running ? " is-running" : ""}`}
          aria-hidden="true"
        />
        <span className="work-thread-row-body">
          <span className="work-thread-row-title">
            {thread.title || "Untitled thread"}
          </span>
          <span className="work-thread-row-time">
            {formatDate(thread.lastMessageAt)}
          </span>
        </span>
      </Link>
      <button
        type="button"
        className="work-thread-delete"
        title="Delete thread"
        aria-label="Delete thread"
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          onDelete();
        }}
      >
        <Trash2 size={13} strokeWidth={2} />
      </button>
    </div>
  );
}
