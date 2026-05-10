"use client";

import "@/a2ui/components";
import {
  ArrowUp,
  Brain,
  Check,
  Loader2,
  Paperclip,
  Plus,
  Sparkles,
  Square,
  Trash2,
  X,
} from "lucide-react";
import Link from "next/link";
import ReactMarkdown from "react-markdown";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import { confirmDialog } from "@/components/ConfirmModal";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { renderComponent, renderChildren } from "@/a2ui/renderer";
import { applyMessage, getRootComponent } from "@/a2ui/surface";
import type { SurfaceState, A2UIMessage } from "@/a2ui/types";

type ThreadSummary = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  lastMessageAt: string;
};

type MessageRecord = {
  id: string;
  runId: string | null;
  role: "user" | "assistant";
  content: string;
  createdAt: string;
};

type RunRecord = {
  id: string;
  backend: "hermes" | "claude-agent";
  status: string;
  error: string | null;
  createdAt: string;
  finishedAt: string | null;
};

type WorkEvent =
  | { type: "hello"; runId: string; threadId: string; backend?: RunRecord["backend"] }
  | { type: "message"; role: "user" | "assistant"; content: string }
  | { type: "tool_start"; id: string; name: string; input?: unknown }
  | { type: "tool_delta"; id: string; delta: unknown }
  | { type: "tool_end"; id: string; result?: unknown; error?: string }
  | { type: "surface"; messages: A2UIMessage[] }
  | { type: "artifact"; artifact: { path: string; label: string; mimeType?: string } }
  | { type: "status"; message: string }
  | { type: "error"; message: string }
  | { type: "done"; result?: unknown };

type ThreadBundle = {
  thread: {
    id: string;
    title: string;
    createdAt: string;
    updatedAt: string;
    lastMessageAt: string;
  };
  runs: RunRecord[];
  messages: MessageRecord[];
  eventsByRun: Record<string, WorkEvent[]>;
};

type UploadedWorkFile = {
  name: string;
  relativePath: string;
  absolutePath: string;
};

type MemoryRecord = {
  id: string;
  kind: string;
  scope: string;
  scopeId: string | null;
  text: string;
  pinned: boolean;
  confidence: number;
};

type PendingMemory = {
  id: string;
  draftText: string;
  draftKind: string;
  draftScope: string;
  confidence: number;
  reasoning: string | null;
  conflicts: Array<{ memoryId: string; text: string; similarity: number }>;
};

export default function WorkScreen() {
  const router = useRouter();
  const [gateChecked, setGateChecked] = useState(false);
  const [gateError, setGateError] = useState<string | null>(null);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [bundle, setBundle] = useState<ThreadBundle | null>(null);
  const [memories, setMemories] = useState<MemoryRecord[]>([]);
  const [pendingMemories, setPendingMemories] = useState<PendingMemory[]>([]);
  const [draft, setDraft] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [loadingThreads, setLoadingThreads] = useState(true);
  const [loadingThread, setLoadingThread] = useState(false);
  const [sending, setSending] = useState(false);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/onboarding/status");
        const status = await res.json().catch(() => ({ state: "db_error", message: "Could not reach server" }));
        if (cancelled) return;
        if (status.state === "db_error") {
          setGateError(status.message ?? "Database unavailable");
          setGateChecked(true);
          return;
        }
        if (status.state === "needs_wizard") {
          router.replace("/onboarding");
          return;
        }
        if (status.state === "failed") {
          router.replace("/onboarding?failed=1");
          return;
        }
        if (status.state === "processing") {
          router.replace("/business-profile");
          return;
        }
        setGateChecked(true);
      } catch (err) {
        if (cancelled) return;
        setGateError(err instanceof Error ? err.message : "Could not reach server");
        setGateChecked(true);
      }
    })();
    return () => { cancelled = true; };
  }, [router]);

  useEffect(() => {
    if (!gateChecked || gateError) return;
    void loadThreads();
    void loadMemories();
  }, [gateChecked, gateError]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [bundle, sending, activeRunId]);

  async function loadThreads(preferredThreadId?: string) {
    setLoadingThreads(true);
    try {
      const res = await fetch("/api/work/threads");
      const data = (await res.json()) as { threads: ThreadSummary[] };
      setThreads(data.threads ?? []);
      const nextId =
        preferredThreadId ??
        activeThreadId ??
        data.threads?.[0]?.id ??
        null;
      setActiveThreadId(nextId);
      if (nextId) {
        await loadThread(nextId);
      } else {
        setBundle(null);
      }
    } finally {
      setLoadingThreads(false);
    }
  }

  async function loadThread(threadId: string) {
    setLoadingThread(true);
    try {
      const res = await fetch(`/api/work/threads/${threadId}`);
      if (!res.ok) {
        setBundle(null);
        return;
      }
      const data = (await res.json()) as ThreadBundle;
      setBundle(data);
      setActiveThreadId(threadId);
      await loadPendingMemories(threadId);
    } finally {
      setLoadingThread(false);
    }
  }

  async function deleteThread(threadId: string) {
    const target = threads.find((t) => t.id === threadId);
    const label = target?.title || "Untitled thread";
    const ok = await confirmDialog({
      title: `Delete "${label}"?`,
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
      const nextId = remaining[0]?.id ?? null;
      setActiveThreadId(nextId);
      if (nextId) {
        await loadThread(nextId);
      } else {
        setBundle(null);
      }
    }
  }

  async function loadMemories() {
    const res = await fetch("/api/work/memories");
    if (!res.ok) return;
    const data = (await res.json()) as { memories: MemoryRecord[] };
    setMemories(data.memories ?? []);
  }

  async function loadPendingMemories(threadId: string) {
    const res = await fetch(`/api/work/memories/pending?threadId=${encodeURIComponent(threadId)}`);
    if (!res.ok) return;
    const data = (await res.json()) as { pending: PendingMemory[] };
    setPendingMemories(data.pending ?? []);
  }

  async function createThread(): Promise<string> {
    const res = await fetch("/api/work/threads", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    const data = (await res.json()) as { thread: ThreadSummary };
    const nextId = data.thread.id;
    setThreads((prev) => [data.thread, ...prev]);
    setActiveThreadId(nextId);
    setBundle({
      thread: {
        id: data.thread.id,
        title: data.thread.title,
        createdAt: data.thread.createdAt,
        updatedAt: data.thread.updatedAt,
        lastMessageAt: data.thread.lastMessageAt,
      },
      runs: [],
      messages: [],
      eventsByRun: {},
    });
    setPendingMemories([]);
    return nextId;
  }

  async function uploadFiles(threadId: string, picked: File[]): Promise<UploadedWorkFile[]> {
    const uploaded: UploadedWorkFile[] = [];
    for (const file of picked) {
      const body = new FormData();
      body.append("threadId", threadId);
      body.append("file", file);
      const res = await fetch("/api/work/upload", { method: "POST", body });
      if (!res.ok) continue;
      const data = (await res.json()) as { file: UploadedWorkFile };
      uploaded.push(data.file);
    }
    return uploaded;
  }

  async function sendMessage() {
    const trimmed = draft.trim();
    if (!trimmed && files.length === 0) return;

    setSending(true);
    setStreamError(null);

    let threadId = activeThreadId;
    if (!threadId) {
      threadId = await createThread();
    }

    const uploads = threadId ? await uploadFiles(threadId, files) : [];
    const message = joinMessageWithAttachments(trimmed, uploads);
    const tempMessage: MessageRecord = {
      id: `temp-${Date.now()}`,
      runId: null,
      role: "user",
      content: message,
      createdAt: new Date().toISOString(),
    };
    setBundle((prev) =>
      prev
        ? { ...prev, messages: [...prev.messages, tempMessage] }
        : prev,
    );
    setDraft("");
    setFiles([]);

    const res = await fetch(`/api/work/threads/${threadId}/runs`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message }),
    });
    if (!res.ok) {
      const errBody = await res.json().catch(() => ({}));
      setSending(false);
      setStreamError(errBody.error ?? `HTTP ${res.status}`);
      await loadThread(threadId);
      return;
    }
    const { runId, backend } = (await res.json()) as {
      runId: string;
      backend: string;
    };

    setActiveRunId(runId);
    setBundle((prev) =>
      prev
        ? {
            ...prev,
            messages: prev.messages.map((message, index) =>
              index === prev.messages.length - 1 &&
              message.role === "user" &&
              message.runId === null
                ? { ...message, runId }
                : message,
            ),
            runs: [
              ...prev.runs,
              {
                id: runId,
                backend: (backend as "hermes" | "claude-agent") ?? "hermes",
                status: "running",
                error: null,
                createdAt: new Date().toISOString(),
                finishedAt: null,
              },
            ],
          }
        : prev,
    );

    await new Promise<void>((resolve) => {
      const es = new EventSource(
        `/api/work/threads/${threadId}/runs/${runId}/events`,
      );
      const finish = () => {
        es.close();
        resolve();
      };
      es.onmessage = (msgEvent) => {
        let event: WorkEvent;
        try {
          event = JSON.parse(msgEvent.data) as WorkEvent;
        } catch {
          return;
        }
        if (event.type === "hello") return;
        applyIncomingEvent(runId, event);
        if (event.type === "done") finish();
      };
      es.onerror = () => {};
    });

    setSending(false);
    setActiveRunId(null);
    await Promise.all([loadThreads(threadId), loadThread(threadId), loadMemories()]);
    window.setTimeout(() => {
      void loadPendingMemories(threadId);
      void loadMemories();
    }, 1500);
  }

  function applyIncomingEvent(runId: string, event: WorkEvent) {
    if (event.type === "message" && event.role === "assistant") {
      setBundle((prev) => {
        if (!prev) return prev;
        const already = prev.messages.find(
          (message) => message.runId === runId && message.role === "assistant",
        );
        const nextMessages: MessageRecord[] = already
          ? prev.messages.map((message) =>
              message.runId === runId && message.role === "assistant"
                ? { ...message, content: event.content }
                : message,
            )
          : [
              ...prev.messages,
              {
                id: `assistant-${runId}`,
                runId,
                role: "assistant",
                content: event.content,
                createdAt: new Date().toISOString(),
              },
            ];
        return { ...prev, messages: nextMessages };
      });
    }

    setBundle((prev) => {
      if (!prev) return prev;
      const currentEvents = prev.eventsByRun[runId] ?? [];
      const nextEvents =
        event.type === "done"
          ? [...currentEvents, event]
          : [...currentEvents, event];
      const nextRuns = prev.runs.map((run) =>
        run.id === runId
          ? {
              ...run,
              status:
                event.type === "done"
                  ? String((event.result as { status?: string } | undefined)?.status ?? run.status)
                  : event.type === "error"
                  ? "failed"
                  : run.status,
              error: event.type === "error" ? event.message : run.error,
            }
          : run,
      );
      return {
        ...prev,
        runs: nextRuns,
        eventsByRun: {
          ...prev.eventsByRun,
          [runId]: nextEvents,
        },
      };
    });

    if (event.type === "error") {
      setStreamError(event.message);
    }
  }

  async function cancelRun() {
    if (!activeRunId) return;
    await fetch(`/api/work/runs/${activeRunId}/cancel`, { method: "POST" });
  }

  async function decidePendingMemory(
    id: string,
    action: "accept" | "decline",
    overrides: { scope?: string; scopeId?: string | null } = {},
  ) {
    const res = await fetch("/api/work/memories/decide", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, action, ...overrides }),
    });
    if (!res.ok) return;
    if (activeThreadId) await loadPendingMemories(activeThreadId);
    await loadMemories();
  }

  const runLookup = useMemo(() => {
    const map = new Map<string, RunRecord>();
    for (const run of bundle?.runs ?? []) map.set(run.id, run);
    return map;
  }, [bundle?.runs]);

  const latestRunId = bundle?.runs.at(-1)?.id ?? activeRunId;

  if (!gateChecked) {
    return (
      <>
        <div className="root">
          <AppHeader>
            <SectionNav current="work" />
          </AppHeader>
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
            Loading…
          </div>
        </div>
        <CreatorCredit />
      </>
    );
  }

  if (gateError) {
    return (
      <>
        <div className="root">
          <AppHeader>
            <SectionNav current="work" />
          </AppHeader>
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
            <div style={{ marginBottom: 8, color: "var(--text2)" }}>
              Can&apos;t reach the database right now.
            </div>
            <div style={{ fontSize: 13 }}>
              Work will load once the connection is back.
            </div>
            <button
              onClick={() => { setGateError(null); setGateChecked(false); window.location.reload(); }}
              style={{ marginTop: 16, padding: "6px 14px", fontSize: 13, cursor: "pointer" }}
            >
              Retry
            </button>
          </div>
        </div>
        <CreatorCredit />
      </>
    );
  }

  return (
    <>
      <div className="root">
        <AppHeader>
          <SectionNav current="work" />
        </AppHeader>

        <div className="work-layout">
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
                <div className="work-empty">Loading threads…</div>
              ) : threads.length === 0 ? (
                <div className="work-empty">Start a thread to use Work.</div>
              ) : (
                threads.map((thread) => (
                  <ThreadRow
                    key={thread.id}
                    thread={thread}
                    active={thread.id === activeThreadId}
                    running={thread.id === activeThreadId && Boolean(activeRunId)}
                    onSelect={() => void loadThread(thread.id)}
                    onDelete={() => void deleteThread(thread.id)}
                  />
                ))
              )}
            </div>

            <div className="work-sidebar-footer">
              <Link href="/skills" className="work-sidebar-link">
                <Sparkles size={14} strokeWidth={2} />
                <span>Skills</span>
              </Link>
              <Link href="/memory" className="work-sidebar-link">
                <Brain size={14} strokeWidth={2} />
                <span>Memory</span>
                {pendingMemories.length > 0 ? (
                  <span className="work-sidebar-badge">{pendingMemories.length}</span>
                ) : null}
              </Link>
            </div>
          </aside>

          <section className="work-panel">
            <div className="work-panel-head">
              <div className="work-thread-head">
                <span className="work-thread-eyebrow">Thread</span>
                <span className="work-thread-title" title={bundle?.thread.title || "Work"}>
                  {bundle?.thread.title || "Work"}
                </span>
              </div>
            </div>

            <div className="work-transcript">
              {loadingThread ? (
                <div className="work-empty">Loading thread…</div>
              ) : bundle?.messages.length ? (
                bundle.messages.map((message, index) => (
                  <div key={`${message.id}-${index}`} className="work-turn">
                    {message.role === "user" ? (
                      <MessageBubble message={message} />
                    ) : null}
                    {message.role === "user" && message.runId ? (
                      <RunActivity
                        run={runLookup.get(message.runId) ?? null}
                        events={bundle.eventsByRun[message.runId] ?? []}
                        pending={sending && latestRunId === message.runId}
                      />
                    ) : null}
                    {message.role === "assistant" && message.runId ? (
                      <RunSurfaces events={bundle.eventsByRun[message.runId] ?? []} />
                    ) : null}
                    {message.role === "assistant" ? (
                      <MessageBubble message={message} />
                    ) : null}
                  </div>
                ))
              ) : null}

              {streamError ? (
                <div className="work-error">{streamError}</div>
              ) : null}
              <div ref={endRef} />
            </div>

            <div className="work-composer">
              {pendingMemories.length > 0 ? (
                <PendingMemoryPanel
                  pending={pendingMemories}
                  threadId={activeThreadId}
                  onDecide={(id, action, overrides) =>
                    void decidePendingMemory(id, action, overrides)
                  }
                />
              ) : null}

              {files.length > 0 ? (
                <div className="work-files">
                  {files.map((file, index) => (
                    <div key={`${file.name}-${index}`} className="work-file-chip">
                      <span>{file.name}</span>
                      <button
                        type="button"
                        onClick={() =>
                          setFiles((prev) => prev.filter((_, fileIndex) => fileIndex !== index))
                        }
                      >
                        ×
                      </button>
                    </div>
                  ))}
                </div>
              ) : null}

              <div className="work-composer-shell">
                <button
                  className="work-icon-btn"
                  onClick={() => fileInputRef.current?.click()}
                  title="Attach files"
                  type="button"
                >
                  <Paperclip size={16} strokeWidth={2} />
                </button>
                <textarea
                  className="work-input"
                  placeholder={sending ? "Working…" : "Ask a question or attach a file…"}
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" && !event.shiftKey && !sending) {
                      event.preventDefault();
                      void sendMessage();
                    }
                  }}
                  disabled={sending}
                  rows={1}
                />
                {sending ? (
                  <button className="work-send-btn is-stop" type="button" onClick={() => void cancelRun()}>
                    <Square size={15} fill="currentColor" strokeWidth={2} />
                  </button>
                ) : (
                  <button className="work-send-btn" type="button" onClick={() => void sendMessage()}>
                    <ArrowUp size={16} strokeWidth={2.25} />
                  </button>
                )}
              </div>

              <input
                ref={fileInputRef}
                type="file"
                multiple
                hidden
                onChange={(event) => {
                  const picked = Array.from(event.target.files ?? []);
                  setFiles((prev) => [...prev, ...picked].slice(0, 5));
                  event.target.value = "";
                }}
              />
            </div>
          </section>
        </div>
      </div>

      <CreatorCredit />
    </>
  );
}

function PendingMemoryPanel({
  pending,
  threadId,
  onDecide,
}: {
  pending: PendingMemory[];
  threadId: string | null;
  onDecide: (
    id: string,
    action: "accept" | "decline",
    overrides?: { scope?: string; scopeId?: string | null },
  ) => void;
}) {
  const item = pending[0];
  if (!item) return null;
  return (
    <div className="work-memory-prompt">
      <div className="work-memory-prompt-copy">
        <div className="work-memory-kind">Memory suggestion</div>
        <div>{item.draftText}</div>
        {pending.length > 1 ? (
          <div className="work-memory-prompt-count">+{pending.length - 1} more</div>
        ) : null}
      </div>
      <div className="work-memory-prompt-actions">
        <button type="button" onClick={() => onDecide(item.id, "decline")} title="Dismiss">
          <X size={14} />
        </button>
        <button
          type="button"
          onClick={() => onDecide(item.id, "accept", { scope: "global" })}
          title="Save globally"
        >
          <Check size={14} />
          <span>Global</span>
        </button>
        {threadId ? (
          <button
            type="button"
            onClick={() =>
              onDecide(item.id, "accept", { scope: "thread", scopeId: threadId })
            }
            title="Save for this thread only"
          >
            <Check size={14} />
            <span>Thread</span>
          </button>
        ) : null}
      </div>
    </div>
  );
}

function ThreadRow({
  thread,
  active,
  running,
  onSelect,
  onDelete,
}: {
  thread: ThreadSummary;
  active: boolean;
  running: boolean;
  onSelect: () => void;
  onDelete: () => void;
}) {
  return (
    <div className={`work-thread-row${active ? " is-active" : ""}`}>
      <button
        type="button"
        className="work-thread-row-main"
        onClick={onSelect}
        title={thread.title || "Untitled thread"}
      >
        <span className={`work-thread-dot${running ? " is-running" : ""}`} aria-hidden="true" />
        <span className="work-thread-row-body">
          <span className="work-thread-row-title">{thread.title || "Untitled thread"}</span>
          <span className="work-thread-row-time">{formatDate(thread.lastMessageAt)}</span>
        </span>
      </button>
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

function MessageBubble({ message }: { message: MessageRecord }) {
  if (message.role === "user") {
    return (
      <div className="work-bubble-row is-user">
        <div className="work-bubble is-user">
          <div className="work-markdown user-copy">{message.content}</div>
        </div>
      </div>
    );
  }
  return (
    <div className="work-bubble-row">
      <div className="work-bubble">
        <div className="work-markdown">
          <ReactMarkdown>{message.content}</ReactMarkdown>
        </div>
      </div>
    </div>
  );
}

function RunActivity({
  run,
  events,
  pending,
}: {
  run: RunRecord | null;
  events: WorkEvent[];
  pending: boolean;
}) {
  const items = useMemo(() => buildActivityItems(events), [events]);

  if (items.length === 0 && !pending && !run) return null;

  return (
    <div className="work-activity">
      {items.map((item, index) => {
        if (item.kind === "tools") {
          return <ToolGroup key={`tools-${index}`} tools={item.tools} />;
        }
        if (item.kind === "status") {
          return (
            <div key={`status-${index}`} className="work-status-row">
              <span>{item.message}</span>
            </div>
          );
        }
        return (
          <div key={`error-${index}`} className="work-error">
            {item.message}
          </div>
        );
      })}
      {pending ? (
        <div className="work-status-row">
          <Loader2 className="work-status-spin" size={12} />
          <span>Running…</span>
        </div>
      ) : null}
      {run?.error ? <div className="work-error">{run.error}</div> : null}
    </div>
  );
}

type ToolItem = {
  id: string;
  name: string;
  input?: unknown;
  deltas: unknown[];
  end?: Extract<WorkEvent, { type: "tool_end" }>;
};

type ActivityItem =
  | { kind: "tools"; tools: ToolItem[] }
  | { kind: "status"; message: string }
  | { kind: "error"; message: string };

function buildActivityItems(events: WorkEvent[]): ActivityItem[] {
  const items: ActivityItem[] = [];
  const toolsById = new Map<string, ToolItem>();
  let openGroup: ToolItem[] | null = null;
  let lastStatus: string | null = null;

  const closeGroup = () => {
    if (openGroup && openGroup.length > 0) {
      items.push({ kind: "tools", tools: openGroup });
    }
    openGroup = null;
  };

  for (const event of events) {
    switch (event.type) {
      case "tool_start": {
        if (!openGroup) openGroup = [];
        const item: ToolItem = {
          id: event.id,
          name: event.name,
          input: event.input,
          deltas: [],
        };
        toolsById.set(event.id, item);
        openGroup.push(item);
        break;
      }
      case "tool_delta": {
        const item = toolsById.get(event.id);
        if (item) item.deltas.push(event.delta);
        break;
      }
      case "tool_end": {
        const item = toolsById.get(event.id);
        if (item) item.end = event;
        break;
      }
      case "status": {
        if (event.message === lastStatus) break;
        closeGroup();
        items.push({ kind: "status", message: event.message });
        lastStatus = event.message;
        break;
      }
      case "error": {
        closeGroup();
        items.push({ kind: "error", message: event.message });
        break;
      }
      default:
        break;
    }
  }
  closeGroup();

  // Keep only the most recent status pill — earlier ones are stale state
  // transitions and clutter the timeline once tool work has happened.
  let lastStatusIdx = -1;
  for (let i = 0; i < items.length; i++) {
    if (items[i].kind === "status") lastStatusIdx = i;
  }
  return lastStatusIdx === -1
    ? items
    : items.filter((it, i) => it.kind !== "status" || i === lastStatusIdx);
}

function ToolGroup({ tools }: { tools: ToolItem[] }) {
  const inflight = tools.filter((t) => !t.end).length;
  const failed = tools.filter((t) => t.end?.error).length;
  const showHeader = tools.length > 1;
  const [open, setOpen] = useState(tools.length <= 2 || inflight > 0);

  useEffect(() => {
    if (inflight > 0) setOpen(true);
  }, [inflight]);

  if (!showHeader) {
    return (
      <div className="work-tool-group work-tool-group-single">
        <ToolRow tool={tools[0]} />
      </div>
    );
  }

  return (
    <div className="work-tool-group">
      <button
        type="button"
        className="work-tool-group-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <span className="work-tool-group-toggle">{open ? "▾" : "▸"}</span>
        <span className="work-tool-group-count">
          {tools.length} tool {tools.length === 1 ? "call" : "calls"}
        </span>
        {inflight > 0 ? (
          <span className="work-tool-group-badge running">
            <Loader2 className="work-status-spin" size={11} /> running
          </span>
        ) : null}
        {failed > 0 ? (
          <span className="work-tool-group-badge failed">
            {failed} failed
          </span>
        ) : null}
      </button>
      {open ? (
        <div className="work-tool-group-body">
          {tools.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ToolRow({ tool }: { tool: ToolItem }) {
  const [open, setOpen] = useState(false);
  const status: "running" | "done" | "failed" = tool.end?.error
    ? "failed"
    : tool.end
      ? "done"
      : "running";
  const subtitle = toolSubtitle(tool);
  const hasDetail =
    tool.input !== undefined ||
    tool.deltas.length > 0 ||
    tool.end?.result !== undefined ||
    tool.end?.error !== undefined;

  return (
    <div className={`work-tool-row work-tool-row-${status}`}>
      <button
        type="button"
        className="work-tool-row-head"
        onClick={() => hasDetail && setOpen((v) => !v)}
        disabled={!hasDetail}
        aria-expanded={open}
      >
        <span className={`work-tool-row-icon work-tool-row-icon-${status}`}>
          {status === "running" ? (
            <Loader2 className="work-status-spin" size={12} />
          ) : status === "failed" ? (
            <X size={12} />
          ) : (
            <Check size={12} />
          )}
        </span>
        <span className="work-tool-row-name">{tool.name}</span>
        {subtitle ? <span className="work-tool-row-subtitle">{subtitle}</span> : null}
      </button>
      {open ? (
        <div className="work-tool-row-detail">
          {tool.input !== undefined ? (
            <>
              <div className="work-tool-row-section-label">Input</div>
              <pre className="work-tool-row-pre">{formatToolPayload(tool.input)}</pre>
            </>
          ) : null}
          {tool.deltas
            .map((d) => describeToolDelta(d))
            .filter(Boolean)
            .map((text, i) => (
              <div key={i} className="work-tool-delta">
                {text}
              </div>
            ))}
          {tool.end?.result ? (
            <>
              <div className="work-tool-row-section-label">Output</div>
              <pre className="work-tool-row-pre">{formatToolPayload(tool.end.result)}</pre>
            </>
          ) : null}
          {tool.end?.error ? (
            <>
              <div className="work-tool-row-section-label">Error</div>
              <pre className="work-tool-row-pre work-tool-row-pre-error">{tool.end.error}</pre>
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function toolSubtitle(tool: ToolItem): string {
  for (const delta of tool.deltas) {
    if (delta && typeof delta === "object" && "summary" in delta) {
      const s = (delta as { summary?: unknown }).summary;
      if (typeof s === "string" && s.trim()) return s.trim();
    }
  }
  if (tool.input && typeof tool.input === "object") {
    const obj = tool.input as Record<string, unknown>;
    if (typeof obj.title === "string" && obj.title.trim()) return obj.title.trim();
    if (typeof obj.command === "string" && obj.command.trim()) return obj.command.trim();
    if (typeof obj.description === "string" && obj.description.trim()) {
      return obj.description.trim();
    }
  }
  return "";
}

function RunSurfaces({ events }: { events: WorkEvent[] }) {
  const messages = events
    .filter((event): event is Extract<WorkEvent, { type: "surface" }> => event.type === "surface")
    .flatMap((event) => event.messages);
  if (messages.length === 0) return null;
  return <SurfaceBlock messages={messages} />;
}

function SurfaceBlock({ messages }: { messages: A2UIMessage[] }) {
  const surfaces = useMemo(() => {
    let next = new Map<string, SurfaceState>();
    for (const message of messages) {
      next = applyMessage(next, message);
    }
    return next;
  }, [messages]);

  const nodes: React.ReactNode[] = [];
  for (const [, surface] of surfaces) {
    const ctx = { surface };
    const root = getRootComponent(surface);
    if (root) {
      nodes.push(
        <div key={surface.surfaceId} className="work-surface-frame">
          {renderComponent(root, ctx)}
        </div>,
      );
      continue;
    }
    const ids = Array.from(surface.components.keys());
    if (!ids.length) continue;
    nodes.push(
      <div key={surface.surfaceId} className="work-surface-frame">
        {renderChildren(ids, ctx)}
      </div>,
    );
  }

  if (nodes.length === 0) return null;
  return <div className="work-surface-stack">{nodes}</div>;
}

function formatDate(value: string): string {
  const date = new Date(value);
  return date.toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
  });
}

function describeToolDelta(delta: unknown): string {
  if (!delta || typeof delta !== "object") {
    return typeof delta === "string" ? delta : "";
  }
  const info = delta as {
    message?: unknown;
    summary?: unknown;
    elapsedSeconds?: unknown;
    durationMs?: unknown;
  };
  if (typeof info.summary === "string" && info.summary.trim()) {
    return info.summary.trim();
  }
  if (typeof info.message === "string" && info.message.trim()) {
    return info.message.trim();
  }
  if (typeof info.durationMs === "number" && Number.isFinite(info.durationMs)) {
    return `Took ${formatDuration(info.durationMs)}`;
  }
  if (typeof info.elapsedSeconds === "number" && Number.isFinite(info.elapsedSeconds)) {
    return `Running for ${info.elapsedSeconds.toFixed(1)}s`;
  }
  return "";
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function formatToolPayload(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value ?? {}, null, 2);
  } catch {
    return String(value);
  }
}

function joinMessageWithAttachments(
  text: string,
  files: UploadedWorkFile[],
): string {
  if (files.length === 0) return text;
  const lines = files.map(
    (file) => `- ${file.relativePath} (${file.name})`,
  );
  const prefix = text.trim() ? `${text.trim()}\n\n` : "";
  return `${prefix}I've attached ${files.length === 1 ? "a file" : "files"}:\n${lines.join("\n")}`;
}
