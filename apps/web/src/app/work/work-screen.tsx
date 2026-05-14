"use client";

import "@/a2ui/components";
import type { AnchorHTMLAttributes, ReactNode } from "react";
import {
  ArrowUp,
  Brain,
  Check,
  Copy,
  Loader2,
  Paperclip,
  Pencil,
  Plus,
  RefreshCw,
  Sparkles,
  Square,
  Trash2,
  X,
} from "lucide-react";
import Link from "next/link";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";

// Matches absolute container paths to agent-generated/uploaded files inside the
// per-org workspace, e.g.
//   /tmp/openneko-home/.config/openneko/agents/orgs/<orgId>/runs/<runId>/artifacts/file.pdf
//   ~/.config/openneko/agents/orgs/<orgId>/uploads/<threadId>/file.csv
// The capture group is the workspace-relative path the /api/work/files route
// already serves.
const WORKSPACE_FILE_RE =
  /\/agents\/orgs\/[A-Za-z0-9-]+\/((?:runs|uploads|skills|memory)\/[A-Za-z0-9._\-/]+\.[A-Za-z0-9]+)/g;

type MdNode = { type: string; value?: string; url?: string; children?: MdNode[] };

function autolinkWorkspaceFiles() {
  return (tree: unknown) => walkMdNode(tree, null);
}

function walkMdNode(node: unknown, parent: MdNode | null): void {
  if (!node || typeof node !== "object") return;
  const n = node as MdNode;
  if (n.type === "code" || n.type === "link") return;
  // inlineCode is the `path` case — assistant often wraps file paths in
  // backticks. Convert the whole inline-code node to a link if its value
  // matches a workspace file path.
  if (n.type === "inlineCode" && typeof n.value === "string" && parent?.children) {
    WORKSPACE_FILE_RE.lastIndex = 0;
    const match = WORKSPACE_FILE_RE.exec(n.value);
    if (match) {
      const idx = parent.children.indexOf(n);
      if (idx !== -1) {
        parent.children.splice(idx, 1, {
          type: "link",
          url: `/api/work/files/${match[1]}`,
          children: [{ type: "inlineCode", value: n.value.split("/").slice(-1)[0] }],
        });
      }
    }
    return;
  }
  if (n.type === "text" && typeof n.value === "string" && parent?.children) {
    const value = n.value;
    WORKSPACE_FILE_RE.lastIndex = 0;
    const matches = [...value.matchAll(WORKSPACE_FILE_RE)];
    if (matches.length === 0) return;
    const replacement: MdNode[] = [];
    let cursor = 0;
    for (const m of matches) {
      const start = m.index ?? 0;
      const end = start + m[0].length;
      if (start > cursor) {
        replacement.push({ type: "text", value: value.slice(cursor, start) });
      }
      replacement.push({
        type: "link",
        url: `/api/work/files/${m[1]}`,
        children: [{ type: "text", value: m[0].split("/").slice(-1)[0] }],
      });
      cursor = end;
    }
    if (cursor < value.length) {
      replacement.push({ type: "text", value: value.slice(cursor) });
    }
    const idx = parent.children.indexOf(n);
    if (idx !== -1) parent.children.splice(idx, 1, ...replacement);
    return;
  }
  if (Array.isArray(n.children)) {
    for (const child of n.children.slice()) walkMdNode(child, n);
  }
}

const REMARK_PLUGINS = [remarkGfm, autolinkWorkspaceFiles];

import {
  WORKSPACE_MARKDOWN_COMPONENTS as MARKDOWN_COMPONENTS,
  linkifyWorkspacePaths,
} from "@/lib/linkify-workspace-paths";
import AppHeader from "@/components/AppHeader";
import BriefingCard from "@/components/BriefingCard";
import { confirmDialog } from "@/components/ConfirmModal";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { parseBriefingCardMessage } from "@/lib/briefing-card-context";
import WorkSidebar from "./WorkSidebar";
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
  // `content` is a delta — concatenating all message events for a run
  // reconstructs the full assistant text. Mirrors `AgentEvent.message` in
  // packages/llm/src/agent-backend.ts.
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

type StreamResult = "done" | "closed";

type ActiveRunStream = {
  threadId: string;
  runId: string;
  close: () => void;
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

function isRunInFlight(run: RunRecord): boolean {
  return run.status === "queued" || run.status === "running";
}

function latestInFlightRun(runs: RunRecord[]): RunRecord | null {
  for (let i = runs.length - 1; i >= 0; i -= 1) {
    if (isRunInFlight(runs[i])) return runs[i];
  }
  return null;
}

function needsAssistantTimelinePlaceholder(
  run: RunRecord,
  events: WorkEvent[] | undefined,
): boolean {
  return isRunInFlight(run) || Boolean(run.error) || Boolean(events?.length);
}

function withAssistantTimelinePlaceholders(bundle: ThreadBundle): ThreadBundle {
  const placeholders: MessageRecord[] = [];
  const assistantRunIds = new Set(
    bundle.messages
      .filter((message) => message.role === "assistant" && message.runId)
      .map((message) => message.runId as string),
  );

  for (const run of bundle.runs) {
    if (assistantRunIds.has(run.id)) continue;
    if (!needsAssistantTimelinePlaceholder(run, bundle.eventsByRun[run.id])) {
      continue;
    }
    placeholders.push({
      id: `assistant-${run.id}`,
      runId: run.id,
      role: "assistant",
      content: "",
      createdAt: run.createdAt,
    });
  }

  if (placeholders.length === 0) return bundle;

  const messages = [...bundle.messages];
  for (const placeholder of placeholders) {
    let insertAt = messages.length;
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      const message = messages[i];
      if (message.runId === placeholder.runId && message.role === "user") {
        insertAt = i + 1;
        break;
      }
    }
    messages.splice(insertAt, 0, placeholder);
  }

  return { ...bundle, messages };
}

export default function WorkScreen({
  initialThreadId,
}: {
  initialThreadId?: string;
} = {}) {
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
  const activeRunStreamRef = useRef<ActiveRunStream | null>(null);
  const activeThreadIdRef = useRef<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeRunStreamRef.current?.close();
      activeRunStreamRef.current = null;
    };
  }, []);

  useEffect(() => {
    activeThreadIdRef.current = activeThreadId;
  }, [activeThreadId]);

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
    void loadThreads(initialThreadId);
    void loadMemories();
  }, [gateChecked, gateError, initialThreadId]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [bundle, sending, activeRunId]);

  async function loadThreads(preferredThreadId?: string) {
    setLoadingThreads(true);
    try {
      const res = await fetch("/api/work/threads");
      const data = (await res.json()) as { threads: ThreadSummary[] };
      setThreads(data.threads ?? []);
      const nextId = preferredThreadId ?? data.threads?.[0]?.id ?? null;
      // No preferredThreadId means we landed on /work bare. Mirror the
      // selection into the URL so the thread is shareable; the resulting
      // navigation re-mounts this screen with the right initialThreadId.
      if (!preferredThreadId && nextId) {
        router.replace(`/work/${nextId}`);
      }
      activeThreadIdRef.current = nextId;
      setActiveThreadId(nextId);
      if (nextId) {
        await loadThread(nextId);
      } else {
        activeRunStreamRef.current?.close();
        activeRunStreamRef.current = null;
        setSending(false);
        setActiveRunId(null);
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
        activeRunStreamRef.current?.close();
        activeRunStreamRef.current = null;
        setSending(false);
        setActiveRunId(null);
        setBundle(null);
        return;
      }
      const data = (await res.json()) as ThreadBundle;
      const nextBundle = withAssistantTimelinePlaceholders(data);
      setBundle(nextBundle);
      activeThreadIdRef.current = threadId;
      setActiveThreadId(threadId);
      resumeLatestInFlightRun(threadId, nextBundle);
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
      router.replace(nextId ? `/work/${nextId}` : "/work");
      activeThreadIdRef.current = nextId;
      setActiveThreadId(nextId);
      if (nextId) {
        await loadThread(nextId);
      } else {
        activeRunStreamRef.current?.close();
        activeRunStreamRef.current = null;
        setSending(false);
        setActiveRunId(null);
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
    activeThreadIdRef.current = nextId;
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
    router.replace(`/work/${nextId}`);
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

    await postAndStreamRun(threadId, message);
  }

  async function copyUserMessage(text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // clipboard permission denied or insecure context — silently no-op
    }
  }

  async function retryOrEditUserMessage(
    messageId: string,
    text: string,
  ): Promise<void> {
    if (sending) return;
    if (!activeThreadId || !text.trim()) return;
    const threadId = activeThreadId;
    setSending(true);
    setStreamError(null);
    if (activeRunId) {
      await fetch(`/api/work/runs/${activeRunId}/cancel`, { method: "POST" }).catch(
        () => {},
      );
    }
    const res = await fetch(`/api/work/threads/${threadId}/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ messageId }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      setSending(false);
      setStreamError(err.error ?? `Could not truncate (HTTP ${res.status})`);
      return;
    }
    await loadThread(threadId);
    setBundle((prev) =>
      prev
        ? {
            ...prev,
            messages: [
              ...prev.messages,
              {
                id: `temp-${Date.now()}`,
                runId: null,
                role: "user",
                content: text,
                createdAt: new Date().toISOString(),
              },
            ],
          }
        : prev,
    );
    await postAndStreamRun(threadId, text);
  }

  async function postAndStreamRun(threadId: string, message: string) {
    const body = JSON.stringify({ message });
    const res = await fetch(`/api/work/threads/${threadId}/runs`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      ...(body.length < 60_000 ? { keepalive: true } : {}),
      body,
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
    if (!mountedRef.current) return;

    setActiveRunId(runId);
    setBundle((prev) =>
      prev
        ? {
            ...prev,
            messages: [
              ...prev.messages.map((message, index) =>
                index === prev.messages.length - 1 &&
                message.role === "user" &&
                message.runId === null
                  ? { ...message, runId }
                  : message,
              ),
              {
                id: `assistant-${runId}`,
                runId,
                role: "assistant" as const,
                content: "",
                createdAt: new Date().toISOString(),
              },
            ],
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

    await streamAndRefreshRun(threadId, runId, 0);
  }

  function resumeLatestInFlightRun(threadId: string, nextBundle: ThreadBundle) {
    const run = latestInFlightRun(nextBundle.runs);
    const current = activeRunStreamRef.current;
    if (current && (!run || current.threadId !== threadId || current.runId !== run.id)) {
      current.close();
    }

    if (!run) {
      if (activeThreadIdRef.current === threadId) {
        setSending(false);
        setActiveRunId(null);
      }
      return;
    }

    setStreamError(null);
    setSending(true);
    setActiveRunId(run.id);
    const afterSeq = nextBundle.eventsByRun[run.id]?.length ?? 0;
    void streamAndRefreshRun(threadId, run.id, afterSeq);
  }

  async function streamAndRefreshRun(
    threadId: string,
    runId: string,
    afterSeq: number,
  ) {
    const result = await followRunEvents(threadId, runId, afterSeq);
    if (result !== "done" || !mountedRef.current) return;
    if (activeThreadIdRef.current !== threadId) return;

    setSending(false);
    setActiveRunId(null);
    await Promise.all([loadThreads(threadId), loadThread(threadId), loadMemories()]);
    window.setTimeout(() => {
      if (!mountedRef.current || activeThreadIdRef.current !== threadId) return;
      void loadPendingMemories(threadId);
      void loadMemories();
    }, 1500);
  }

  async function followRunEvents(
    threadId: string,
    runId: string,
    afterSeq: number,
  ): Promise<StreamResult> {
    const current = activeRunStreamRef.current;
    if (current?.runId === runId && current.threadId === threadId) {
      return "closed";
    }

    current?.close();

    const params = afterSeq > 0 ? `?afterSeq=${afterSeq}` : "";
    return new Promise<StreamResult>((resolve) => {
      let settled = false;
      const es = new EventSource(
        `/api/work/threads/${threadId}/runs/${runId}/events${params}`,
      );
      const settle = (result: StreamResult) => {
        if (settled) return;
        settled = true;
        es.close();
        const active = activeRunStreamRef.current;
        if (active?.runId === runId && active.threadId === threadId) {
          activeRunStreamRef.current = null;
        }
        resolve(result);
      };

      activeRunStreamRef.current = {
        threadId,
        runId,
        close: () => settle("closed"),
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
        if (event.type === "done") settle("done");
      };
      es.onerror = () => {
        // EventSource will reconnect automatically, carrying Last-Event-ID
        // when the server closes a long poll before the worker is finished.
      };
    });
  }

  function applyIncomingEvent(runId: string, event: WorkEvent) {
    if (event.type === "message" && event.role === "assistant") {
      // `content` is a delta — append to the assistant message's running text
      // (the placeholder created in sendMessage already exists with content "").
      setBundle((prev) => {
        if (!prev) return prev;
        return {
          ...prev,
          messages: prev.messages.map((message) =>
            message.runId === runId && message.role === "assistant"
              ? { ...message, content: message.content + event.content }
              : message,
          ),
        };
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
          <WorkSidebar activeRunId={activeRunId} />

          <section className="work-panel">
            <div className="work-transcript">
              {loadingThread ? (
                <div className="work-empty">Loading thread…</div>
              ) : bundle?.messages.length ? (
                bundle.messages.flatMap((message, index, arr) => {
                  if (message.role === "assistant" && message.runId) {
                    // Assistant turns are reconstructed chronologically from the
                    // run event stream (text segments interleaved with tool calls
                    // and the final surface artifact). The persisted message
                    // content is the fallback for runs missing event history.
                    const events = bundle.eventsByRun[message.runId] ?? [];
                    const run = runLookup.get(message.runId) ?? null;
                    const isPending =
                      (run ? isRunInFlight(run) : false) ||
                      (sending && activeRunId === message.runId);
                    return (
                      <div key={`${message.id}-${index}`} className="work-turn">
                        <RunTimeline
                          run={run}
                          events={events}
                          pending={isPending}
                          fallbackContent={message.content}
                        />
                      </div>
                    );
                  }
                  const isPersistedUser =
                    message.role === "user" &&
                    !!message.runId &&
                    !message.id.startsWith("temp-");
                  // Briefing-card context messages: when the user opens a
                  // deep-dive from the dashboard, the seed message stored at
                  // thread-creation time encodes the full card payload after
                  // the BRIEFING_CARD_SENTINEL marker. Render it as a real
                  // BriefingCard so the user sees the same chrome they
                  // clicked, not the raw JSON.
                  const briefingCardCtx =
                    message.role === "user"
                      ? parseBriefingCardMessage(message.content)
                      : null;
                  // If this user message's run terminated (cancelled/failed)
                  // and the next message is NOT the assistant reply for it,
                  // render a status badge so the cancelled run is visible.
                  const orphanRun =
                    message.role === "user" && message.runId
                      ? runLookup.get(message.runId)
                      : null;
                  const nextIsAssistantForRun =
                    arr[index + 1]?.role === "assistant" &&
                    arr[index + 1]?.runId === message.runId;
                  const showRunBadge =
                    orphanRun &&
                    !nextIsAssistantForRun &&
                    (orphanRun.status === "cancelled" ||
                      orphanRun.status === "failed");
                  return [
                    <div key={`${message.id}-${index}`} className="work-turn">
                      {briefingCardCtx ? (
                        <div className="work-seed-context">
                          <div className="work-seed-eyebrow">
                            <span aria-hidden="true" className="work-seed-eyebrow-rule" />
                            From your briefing
                          </div>
                          <BriefingCard ins={briefingCardCtx} index={0} />
                        </div>
                      ) : (
                        <MessageBubble
                          message={message}
                          onCopy={
                            isPersistedUser
                              ? () => void copyUserMessage(message.content)
                              : undefined
                          }
                          onRetry={
                            isPersistedUser && !sending
                              ? () =>
                                  void retryOrEditUserMessage(
                                    message.id,
                                    message.content,
                                  )
                              : undefined
                          }
                          onEdit={
                            isPersistedUser && !sending
                              ? (text) =>
                                  void retryOrEditUserMessage(message.id, text)
                              : undefined
                          }
                        />
                      )}
                    </div>,
                    ...(showRunBadge && orphanRun
                      ? [
                          <div
                            key={`run-status-${orphanRun.id}`}
                            className={`work-run-status work-run-status-${orphanRun.status}`}
                          >
                            {orphanRun.status === "cancelled"
                              ? "Cancelled by user"
                              : `Run failed${orphanRun.error ? `: ${orphanRun.error}` : ""}`}
                          </div>,
                        ]
                      : []),
                  ];
                })
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

function MessageBubble({
  message,
  onCopy,
  onRetry,
  onEdit,
}: {
  message: MessageRecord;
  onCopy?: () => void;
  onRetry?: () => void;
  onEdit?: (text: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [editText, setEditText] = useState(message.content);
  const [copied, setCopied] = useState(false);

  if (message.role !== "user") {
    return (
      <div className="work-bubble-row">
        <div className="work-bubble">
          <div className="work-markdown">
            <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={MARKDOWN_COMPONENTS}>{linkifyWorkspacePaths(message.content)}</ReactMarkdown>
          </div>
        </div>
      </div>
    );
  }

  if (editing && onEdit) {
    const trimmed = editText.trim();
    const dirty = trimmed.length > 0 && trimmed !== message.content.trim();
    const cancel = () => {
      setEditing(false);
      setEditText(message.content);
    };
    const save = () => {
      if (!dirty) return;
      setEditing(false);
      onEdit(trimmed);
    };
    return (
      <div className="work-bubble-row is-user">
        <div className="work-bubble is-user is-editing">
          <textarea
            className="work-bubble-edit"
            value={editText}
            onChange={(e) => {
              setEditText(e.target.value);
              const el = e.currentTarget;
              el.style.height = "auto";
              el.style.height = `${Math.min(el.scrollHeight, 320)}px`;
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                cancel();
              } else if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                save();
              }
            }}
            ref={(el) => {
              if (el) {
                el.style.height = "auto";
                el.style.height = `${Math.min(el.scrollHeight, 320)}px`;
              }
            }}
            rows={1}
            autoFocus
          />
        </div>
        <div className="work-bubble-edit-hint">
          <span>Enter to send · Esc to cancel</span>
          <button type="button" onClick={cancel}>
            Cancel
          </button>
          <button
            type="button"
            className="is-primary"
            onClick={save}
            disabled={!dirty}
          >
            Send
          </button>
        </div>
      </div>
    );
  }

  const showActions = onCopy || onRetry || onEdit;
  return (
    <div className="work-bubble-row is-user has-actions">
      <div className="work-bubble is-user">
        <div className="work-markdown user-copy">{message.content}</div>
      </div>
      {showActions ? (
        <div className="work-bubble-actions">
          {onCopy ? (
            <button
              type="button"
              title={copied ? "Copied" : "Copy"}
              aria-label="Copy"
              onClick={() => {
                onCopy();
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1200);
              }}
            >
              {copied ? <Check size={12} /> : <Copy size={12} />}
            </button>
          ) : null}
          {onRetry ? (
            <button
              type="button"
              title="Retry"
              aria-label="Retry"
              onClick={onRetry}
            >
              <RefreshCw size={12} />
            </button>
          ) : null}
          {onEdit ? (
            <button
              type="button"
              title="Edit"
              aria-label="Edit"
              onClick={() => {
                setEditText(message.content);
                setEditing(true);
              }}
            >
              <Pencil size={12} />
            </button>
          ) : null}
        </div>
      ) : null}
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

type TimelineItem =
  | { kind: "text"; content: string }
  | { kind: "tools"; tools: ToolItem[]; followedByText: boolean }
  | { kind: "error"; message: string };

// Walks a run's event stream chronologically and produces an interleaved
// timeline: text segments split at tool boundaries, with each tool placed
// inline where it ran. Backends emit `message` events as deltas (new text
// since the previous event), so segments build by appending — no string
// archaeology needed.
function buildRunTimeline(events: WorkEvent[]): {
  items: TimelineItem[];
  lastStatus: string | null;
  surfaceMessages: A2UIMessage[];
  isDone: boolean;
} {
  const items: TimelineItem[] = [];
  const toolsById = new Map<string, ToolItem>();
  const surfaceMessages: A2UIMessage[] = [];
  let pendingText = "";
  let lastStatus: string | null = null;
  let isDone = false;

  const flushTextSegment = () => {
    if (pendingText.trim()) {
      items.push({ kind: "text", content: pendingText });
      for (const it of items) {
        if (it.kind === "tools") it.followedByText = true;
      }
    }
    pendingText = "";
  };

  for (const event of events) {
    switch (event.type) {
      case "message": {
        if (event.role !== "assistant") break;
        pendingText += event.content;
        break;
      }
      case "tool_start": {
        flushTextSegment();
        const item: ToolItem = {
          id: event.id,
          name: event.name,
          input: event.input,
          deltas: [],
        };
        toolsById.set(event.id, item);
        // Cluster consecutive tool calls (no text/error between them) into a
        // single collapsible group — keeps long tool runs from dominating the
        // transcript while preserving the start of a new group when the model
        // narrates between calls.
        const last = items[items.length - 1];
        if (last && last.kind === "tools") {
          last.tools.push(item);
        } else {
          items.push({ kind: "tools", tools: [item], followedByText: false });
        }
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
        lastStatus = event.message;
        break;
      }
      case "error": {
        flushTextSegment();
        items.push({ kind: "error", message: event.message });
        break;
      }
      case "surface": {
        for (const msg of event.messages) surfaceMessages.push(msg);
        break;
      }
      case "done": {
        isDone = true;
        break;
      }
      default:
        break;
    }
  }
  flushTextSegment();
  // Synthesize an end for any tool that never got a tool_end before the run
  // terminated. Without this, ACP runs that miss the final tool_call_update
  // notification (Hermes occasionally drops it for read tools) leave the
  // cluster stuck on "running" forever.
  if (isDone) {
    for (const tool of toolsById.values()) {
      if (!tool.end) {
        tool.end = {
          type: "tool_end",
          id: tool.id,
          result: undefined,
        };
      }
    }
  }
  return { items, lastStatus, surfaceMessages, isDone };
}

function RunTimeline({
  run,
  events,
  pending,
  fallbackContent,
}: {
  run: RunRecord | null;
  events: WorkEvent[];
  pending: boolean;
  fallbackContent: string;
}) {
  const { items, lastStatus, surfaceMessages } = useMemo(
    () => buildRunTimeline(events),
    [events],
  );

  const hasContent = items.length > 0 || surfaceMessages.length > 0;

  return (
    <div className="work-timeline">
      {!hasContent && !pending && fallbackContent.trim() ? (
        <div className="work-bubble-row">
          <div className="work-bubble">
            <div className="work-markdown">
              <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={MARKDOWN_COMPONENTS}>{linkifyWorkspacePaths(fallbackContent)}</ReactMarkdown>
            </div>
          </div>
        </div>
      ) : null}

      {items.map((item, index) => {
        if (item.kind === "text") {
          return (
            <div key={`text-${index}`} className="work-bubble-row">
              <div className="work-bubble">
                <div className="work-markdown">
                  <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={MARKDOWN_COMPONENTS}>{linkifyWorkspacePaths(item.content)}</ReactMarkdown>
                </div>
              </div>
            </div>
          );
        }
        if (item.kind === "tools") {
          return (
            <ToolGroup
              key={`tools-${index}`}
              tools={item.tools}
              followedByText={item.followedByText}
            />
          );
        }
        return (
          <div key={`error-${index}`} className="work-error">
            {item.message}
          </div>
        );
      })}

      {surfaceMessages.length > 0 ? (
        <SurfaceBlock messages={surfaceMessages} />
      ) : null}

      {pending ? (
        <div className="work-status-row">
          <Loader2 className="work-status-spin" size={12} />
          <span>{lastStatus ?? "Running…"}</span>
        </div>
      ) : null}
      {!pending && run?.error ? <div className="work-error">{run.error}</div> : null}
    </div>
  );
}

function ToolGroup({
  tools,
  followedByText,
}: {
  tools: ToolItem[];
  followedByText: boolean;
}) {
  const inflight = tools.filter((t) => !t.end).length;
  const failed = tools.filter((t) => t.end?.error).length;
  const showHeader = tools.length > 1;
  // Closed by default. Auto-open only while we're still doing work and the
  // agent hasn't started talking yet — once prose lands, fold back. User can
  // always click the header to open.
  const autoOpen = inflight > 0 && !followedByText;
  const [userOpen, setUserOpen] = useState(false);
  const effectiveOpen = userOpen || autoOpen;

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
        onClick={() => setUserOpen((v) => !v)}
        aria-expanded={effectiveOpen}
      >
        <span className="work-tool-group-toggle">{effectiveOpen ? "▾" : "▸"}</span>
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
      {effectiveOpen ? (
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
