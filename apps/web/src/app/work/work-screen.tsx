"use client";

import "@/a2ui/components";
import {
  ArrowUp,
  Loader2,
  Paperclip,
  Plus,
  Square,
  Wrench,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
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
  | { type: "hello"; runId: string; threadId: string }
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

type WorkAssets = {
  skills: Array<{ name: string; path: string }>;
  memory: Array<{ name: string; path: string }>;
};

export default function WorkScreen() {
  const router = useRouter();
  const [gateChecked, setGateChecked] = useState(false);
  const [gateError, setGateError] = useState<string | null>(null);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [bundle, setBundle] = useState<ThreadBundle | null>(null);
  const [assets, setAssets] = useState<WorkAssets>({ skills: [], memory: [] });
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
    void loadAssets();
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
    } finally {
      setLoadingThread(false);
    }
  }

  async function loadAssets() {
    const res = await fetch("/api/work/assets");
    if (!res.ok) return;
    const data = (await res.json()) as WorkAssets;
    setAssets({
      skills: data.skills ?? [],
      memory: data.memory ?? [],
    });
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
    if (!res.ok || !res.body) {
      setSending(false);
      setStreamError(`HTTP ${res.status}`);
      await loadThread(threadId);
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let runId: string | null = null;
    let buffer = "";

    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let boundary = buffer.indexOf("\n\n");
      while (boundary !== -1) {
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        boundary = buffer.indexOf("\n\n");
        const data = frame
          .split("\n")
          .filter((line) => line.startsWith("data: "))
          .map((line) => line.slice(6))
          .join("\n");
        if (!data) continue;
        const event = JSON.parse(data) as WorkEvent;
        if (event.type === "hello") {
          runId = event.runId;
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
                      id: runId!,
                      backend: "hermes",
                      status: "running",
                      error: null,
                      createdAt: new Date().toISOString(),
                      finishedAt: null,
                    },
                  ],
                }
              : prev,
          );
          continue;
        }
        if (!runId) continue;
        applyIncomingEvent(runId, event);
      }
    }

    setSending(false);
    setActiveRunId(null);
    await Promise.all([loadThreads(threadId), loadThread(threadId), loadAssets()]);
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
              <div>
                <div className="label" style={{ marginBottom: 8 }}>Work</div>
                <div className="work-sidebar-title">Threads</div>
              </div>
              <button className="work-icon-btn" onClick={() => void createThread()} title="New thread">
                <Plus size={16} strokeWidth={2} />
              </button>
            </div>

            <div className="work-thread-list">
              {loadingThreads ? (
                <div className="work-empty">Loading threads…</div>
              ) : threads.length === 0 ? (
                <div className="work-empty">Start a thread to use Work.</div>
              ) : (
                threads.map((thread) => (
                  <button
                    key={thread.id}
                    className={`work-thread-row${thread.id === activeThreadId ? " is-active" : ""}`}
                    onClick={() => void loadThread(thread.id)}
                  >
                    <div className="work-thread-row-title">{thread.title || "Untitled thread"}</div>
                    <div className="work-thread-row-time">{formatDate(thread.lastMessageAt)}</div>
                  </button>
                ))
              )}
            </div>

            <div className="work-sidebar-section">
              <div className="work-sidebar-subtitle">Skills</div>
              <div className="work-asset-list">
                {assets.skills.length === 0 ? (
                  <div className="work-empty is-compact">No skills yet.</div>
                ) : (
                  assets.skills.map((skill) => (
                    <a
                      key={skill.name}
                      href={skill.path}
                      target="_blank"
                      rel="noreferrer"
                      className="work-asset-row"
                    >
                      {skill.name}
                    </a>
                  ))
                )}
              </div>
            </div>

            <div className="work-sidebar-section">
              <div className="work-sidebar-subtitle">Memory</div>
              <div className="work-asset-list">
                {assets.memory.length === 0 ? (
                  <div className="work-empty is-compact">No memory files yet.</div>
                ) : (
                  assets.memory.map((entry) => (
                    <a
                      key={entry.name}
                      href={entry.path}
                      target="_blank"
                      rel="noreferrer"
                      className="work-asset-row"
                    >
                      {entry.name}
                    </a>
                  ))
                )}
              </div>
            </div>
          </aside>

          <section className="work-panel">
            <div className="work-panel-head">
              <div>
                <div className="greet" style={{ fontSize: 28, marginBottom: 4 }}>
                  {bundle?.thread.title || "Work"}
                </div>
                <div className="greet-sub" style={{ marginBottom: 0 }}>
                  Structured analysis when it helps, plain chat when it doesn&apos;t.
                </div>
              </div>
            </div>

            <div className="work-transcript">
              {loadingThread ? (
                <div className="work-empty">Loading thread…</div>
              ) : bundle?.messages.length ? (
                bundle.messages.map((message, index) => (
                  <div key={`${message.id}-${index}`} className="work-turn">
                    <MessageBubble message={message} />
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
                  </div>
                ))
              ) : (
                <div className="work-empty">
                  Ask about a KPI, upload a file, or have the agent explore your data.
                </div>
              )}

              {streamError ? (
                <div className="work-error">{streamError}</div>
              ) : null}
              <div ref={endRef} />
            </div>

            <div className="work-composer">
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
  const toolStarts = events.filter((event): event is Extract<WorkEvent, { type: "tool_start" }> => event.type === "tool_start");
  const toolEnds = new Map(
    events
      .filter((event): event is Extract<WorkEvent, { type: "tool_end" }> => event.type === "tool_end")
      .map((event) => [event.id, event]),
  );
  const statuses = events.filter((event): event is Extract<WorkEvent, { type: "status" }> => event.type === "status");
  const errors = events.filter((event): event is Extract<WorkEvent, { type: "error" }> => event.type === "error");

  if (toolStarts.length === 0 && statuses.length === 0 && errors.length === 0 && !pending && !run) {
    return null;
  }

  return (
    <div className="work-activity">
      {statuses.map((status, index) => (
        <div key={`status-${index}`} className="work-status-row">
          {pending ? <Loader2 className="work-status-spin" size={13} /> : <Wrench size={13} />}
          <span>{status.message}</span>
        </div>
      ))}
      {toolStarts.map((call) => (
        <details key={call.id} className="work-tool-row">
          <summary>
            <span>{call.name}</span>
            <span>{toolEnds.get(call.id)?.error ? "failed" : toolEnds.get(call.id) ? "done" : "running"}</span>
          </summary>
          <pre>{JSON.stringify(call.input ?? {}, null, 2)}</pre>
          {toolEnds.get(call.id)?.result ? <pre>{String(toolEnds.get(call.id)?.result ?? "")}</pre> : null}
          {toolEnds.get(call.id)?.error ? <pre>{toolEnds.get(call.id)?.error}</pre> : null}
        </details>
      ))}
      {errors.map((error, index) => (
        <div key={`error-${index}`} className="work-error">
          {error.message}
        </div>
      ))}
      {pending ? <div className="work-status-row"><Loader2 className="work-status-spin" size={13} /><span>Running…</span></div> : null}
      {run?.error ? <div className="work-error">{run.error}</div> : null}
    </div>
  );
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
