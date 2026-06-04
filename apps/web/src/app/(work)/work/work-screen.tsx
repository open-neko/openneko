"use client";

import "@/a2ui/components";
import {
  ArrowUp,
  Check,
  Copy,
  Loader2,
  Paperclip,
  Pencil,
  RefreshCw,
  Square,
  X,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter, useSearchParams } from "next/navigation";

// Matches paths to agent-generated/uploaded files inside the per-org workspace.
// Two shapes:
//   - absolute container paths, e.g.
//       ~/.config/openneko/agents/orgs/<orgId>/uploads/<threadId>/file.csv
//   - bare workspace-relative paths, e.g. `uploads/<threadId>/file.csv` — the
//     same form `joinMessageWithAttachments` emits into the user's outgoing
//     message and that the agent passes back when it cites files.
// Capture group 1 is the workspace-relative path the /api/work/files route serves.
const WORKSPACE_FILE_RE =
  /(?:\/agents\/orgs\/[A-Za-z0-9-]+\/|(?<![A-Za-z0-9._\-/]))((?:runs|uploads|skills|memory)\/[A-Za-z0-9._\-/]+\.[A-Za-z0-9]+)/g;

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
import BriefingCard from "@/components/BriefingCard";
import {
  ActionRequestCard,
  RuleSavedCard,
  WorkflowSavedCard,
  extractActionRequestEvents,
  extractRuleSaveEvent,
  extractWorkflowSaveEvent,
  stripNekoFences,
} from "@/components/RuleChatBubble";
import { parseBriefingCardMessage } from "@/lib/briefing-card-context";
import { renderComponent, renderChildren } from "@/a2ui/renderer";
import { applyMessage, getRootComponent } from "@/a2ui/surface";
import type { SurfaceState, A2UIMessage } from "@/a2ui/types";
import {
  useWorkShell,
  type RailArtifact,
  type RailSource,
  type RailVital,
} from "../work-shell-context";

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
  | {
      type: "action_request_emit";
      action_request_id: string;
      kind: string;
      scope: "internal" | "external";
      risk_level?: string;
      intent?: string;
      summary?: string;
      decision: "auto_approved" | "pending_approval";
    }
  | {
      type: "action_request_result";
      action_request_id: string;
      kind: string;
      status: "succeeded" | "failed" | "rejected";
      outcome?: {
        result?: Record<string, unknown> | null;
        externalRef?: string | null;
        commandOrOperation?: string | null;
      };
      error?: string;
      rejection_reason?: string;
    }
  | { type: "followups"; items: string[] }
  | { type: "vitals"; items: RailVital[] }
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
  size: number;
  relativePath: string;
  absolutePath: string;
};

const MAX_ATTACHMENTS = 5;
const MAX_ATTACHMENT_SIZE = 10 * 1024 * 1024;
const ACCEPTED_ATTACHMENT_EXTENSIONS = [
  ".csv",
  ".docx",
  ".html",
  ".json",
  ".md",
  ".pdf",
  ".pptx",
  ".tsv",
  ".txt",
  ".xlsx",
];
const ACCEPTED_ATTACHMENT_SUFFIXES = new Set(ACCEPTED_ATTACHMENT_EXTENSIONS);

function fileExtension(name: string): string {
  const idx = name.lastIndexOf(".");
  return idx > 0 ? name.slice(idx).toLowerCase() : "";
}

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

export default function WorkScreen() {
  const router = useRouter();
  const params = useParams();
  const searchParams = useSearchParams();
  const routeThreadId =
    typeof params?.threadId === "string" ? params.threadId : null;
  const { setActiveRunId, setRailArtifacts, setRailContext } = useWorkShell();
  const [gateChecked, setGateChecked] = useState(false);
  const [gateError, setGateError] = useState<string | null>(null);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [bundle, setBundle] = useState<ThreadBundle | null>(null);
  const [memories, setMemories] = useState<MemoryRecord[]>([]);
  const [pendingMemories, setPendingMemories] = useState<PendingMemory[]>([]);
  const [draft, setDraft] = useState(() => searchParams?.get("seed") ?? "");
  const [files, setFiles] = useState<File[]>([]);
  const [loadingThread, setLoadingThread] = useState(false);
  const [sending, setSending] = useState(false);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [activeRunId, setActiveRunIdState] = useState<string | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const activeRunStreamRef = useRef<ActiveRunStream | null>(null);
  const activeThreadIdRef = useRef<string | null>(null);
  const mountedRef = useRef(true);

  const updateActiveRunId = (next: string | null) => {
    setActiveRunIdState(next);
    setActiveRunId(next);
  };

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeRunStreamRef.current?.close();
      activeRunStreamRef.current = null;
      setActiveRunId(null);
    };
    // setActiveRunId comes from a stable provider; intentional empty deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
    void loadMemories();
  }, [gateChecked, gateError]);

  // React to URL thread changes. The /work page (no threadId) is the new-thread
  // state: clear the screen and let the composer create a thread on first send.
  useEffect(() => {
    if (!gateChecked || gateError) return;
    if (routeThreadId) {
      if (routeThreadId !== activeThreadIdRef.current) {
        void loadThread(routeThreadId);
      }
      return;
    }
    void resolveLandingThread();
    // loadThread/resolveLandingThread are stable closures over component state.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gateChecked, gateError, routeThreadId]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [bundle, sending, activeRunId]);

  // Derive this thread's rail context from the run's own output. Vitals and
  // follow-ups are channel-agnostic CONTENT the agent emits (the web channel
  // renders them as a tile grid / chips — another channel could read them
  // aloud). Sources touched are parsed from the data tools the agent actually
  // called (a fact about the run); artifacts come from artifact events. The
  // latest answer in the thread wins for vitals and follow-ups.
  useEffect(() => {
    if (!bundle) {
      setRailArtifacts([]);
      setRailContext({ vitals: [], sources: [], followups: [] });
      return;
    }
    const arts: RailArtifact[] = [];
    const seenArt = new Set<string>();
    const sourceMap = new Map<string, RailSource>();
    let vitals: RailVital[] = [];
    let followups: string[] = [];
    const addSource = (raw: string) => {
      const name = raw.trim().toLowerCase();
      if (name.length < 2 || sourceMap.has(name)) return;
      sourceMap.set(name, { name });
    };
    for (const events of Object.values(bundle.eventsByRun)) {
      for (const ev of events) {
        if (ev.type === "artifact" && ev.artifact && !seenArt.has(ev.artifact.path)) {
          seenArt.add(ev.artifact.path);
          arts.push({
            path: ev.artifact.path,
            label: ev.artifact.label,
            mimeType: ev.artifact.mimeType,
          });
        } else if (ev.type === "tool_start") {
          const title =
            typeof (ev.input as { title?: unknown })?.title === "string"
              ? (ev.input as { title: string }).title
              : "";
          if (title) {
            // graphjin table args: {"table":"x"} / {"from_table":"x"} / {"to_table":"x"}
            for (const m of title.matchAll(
              /"(?:from_table|to_table|table)"\s*:\s*"([a-z0-9_]+)"/gi,
            )) {
              addSource(m[1]);
            }
            // execute_graphql root field: {"query":"{ <table>( …
            const q = title.match(/"query"\s*:\s*"\{\s*([a-z_][a-z0-9_]*)/i);
            if (q) addSource(q[1]);
          }
        } else if (ev.type === "followups" && Array.isArray(ev.items)) {
          followups = ev.items;
        } else if (ev.type === "vitals" && Array.isArray(ev.items)) {
          vitals = ev.items;
        }
      }
    }
    setRailArtifacts(arts);
    setRailContext({
      vitals: vitals.slice(0, 4),
      sources: [...sourceMap.values()].slice(0, 6),
      followups,
    });
  }, [bundle, setRailArtifacts, setRailContext]);

  // Auto-grow the textarea up to its max-height (~9 lines); past that the
  // textarea scrolls internally. CSS alone can't do this — `rows={1}` is
  // the floor and there's no `content-size` for textareas. Keep the cap in
  // sync with .work-input { max-height } in globals.css.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 222)}px`;
  }, [draft]);

  function resolveLandingThread() {
    activeThreadIdRef.current = null;
    setActiveThreadId(null);
    activeRunStreamRef.current?.close();
    activeRunStreamRef.current = null;
    setSending(false);
    updateActiveRunId(null);
    setBundle(null);
    setPendingMemories([]);
  }

  async function loadThread(threadId: string) {
    setLoadingThread(true);
    try {
      const res = await fetch(`/api/work/threads/${threadId}`);
      if (!res.ok) {
        activeRunStreamRef.current?.close();
        activeRunStreamRef.current = null;
        setSending(false);
        updateActiveRunId(null);
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
    const data = (await res.json()) as {
      thread: {
        id: string;
        title: string;
        createdAt: string;
        updatedAt: string;
        lastMessageAt: string;
      };
    };
    const nextId = data.thread.id;
    activeThreadIdRef.current = nextId;
    setActiveThreadId(nextId);
    setBundle({
      thread: data.thread,
      runs: [],
      messages: [],
      eventsByRun: {},
    });
    setPendingMemories([]);
    router.replace(`/work/${nextId}`);
    return nextId;
  }

  async function uploadFiles(
    threadId: string,
    picked: File[],
  ): Promise<{ uploaded: UploadedWorkFile[]; errors: string[] }> {
    const uploaded: UploadedWorkFile[] = [];
    const errors: string[] = [];
    for (const file of picked) {
      const body = new FormData();
      body.append("threadId", threadId);
      body.append("file", file);
      let res: Response;
      try {
        res = await fetch("/api/work/upload", { method: "POST", body });
      } catch (err) {
        errors.push(`"${file.name}": ${err instanceof Error ? err.message : "network error"}`);
        continue;
      }
      if (!res.ok) {
        const payload = (await res.json().catch(() => null)) as { error?: string } | null;
        errors.push(`"${file.name}": ${payload?.error ?? `upload failed (HTTP ${res.status})`}`);
        continue;
      }
      const data = (await res.json()) as { file: UploadedWorkFile };
      uploaded.push(data.file);
    }
    return { uploaded, errors };
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

    let uploads: UploadedWorkFile[] = [];
    if (threadId && files.length > 0) {
      const result = await uploadFiles(threadId, files);
      uploads = result.uploaded;
      if (result.errors.length > 0) {
        setStreamError(result.errors.join(" · "));
      }
      if (uploads.length === 0 && result.errors.length > 0) {
        setSending(false);
        return;
      }
    }
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

    updateActiveRunId(runId);
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
        updateActiveRunId(null);
      }
      return;
    }

    setStreamError(null);
    setSending(true);
    updateActiveRunId(run.id);
    // The loaded bundle already holds this in-flight run's persisted events.
    // Resuming replays them from the start (afterId 0), so drop the local copy
    // first — otherwise replayed events append as duplicates (doubled tool rows
    // and assistant text).
    setBundle((prev) =>
      prev
        ? { ...prev, eventsByRun: { ...prev.eventsByRun, [run.id]: [] } }
        : prev,
    );
    void streamAndRefreshRun(threadId, run.id, 0);
  }

  async function streamAndRefreshRun(
    threadId: string,
    runId: string,
    afterId: number,
  ) {
    const result = await followRunEvents(threadId, runId, afterId);
    if (result !== "done" || !mountedRef.current) return;
    if (activeThreadIdRef.current !== threadId) return;

    setSending(false);
    updateActiveRunId(null);
    await Promise.all([loadThread(threadId), loadMemories()]);
    window.setTimeout(() => {
      if (!mountedRef.current || activeThreadIdRef.current !== threadId) return;
      void loadPendingMemories(threadId);
      void loadMemories();
    }, 1500);
  }

  async function followRunEvents(
    threadId: string,
    runId: string,
    afterId: number,
  ): Promise<StreamResult> {
    const current = activeRunStreamRef.current;
    if (current?.runId === runId && current.threadId === threadId) {
      return "closed";
    }

    current?.close();

    const params = afterId > 0 ? `?afterId=${afterId}` : "";
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
      <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
        Loading…
      </div>
    );
  }

  if (gateError) {
    return (
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
    );
  }

  return (
    <>
      <div className="flex-1 min-h-[460px] flex flex-col gap-7 pt-2 pb-6">
        {loadingThread ? (
          <div className="text-text3 text-[13px] py-3 px-1">Loading thread…</div>
        ) : !bundle?.messages.length ? (
          <EmptyAsk
            onPick={(text) => {
              setDraft(text);
              requestAnimationFrame(() => textareaRef.current?.focus());
            }}
          />
        ) : null}
        {bundle?.messages.length ? (
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
                <div key={`${message.id}-${index}`} className="flex flex-col gap-2.5">
                  <RunTimeline
                    threadId={activeThreadId ?? ""}
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
              <div key={`${message.id}-${index}`} className="flex flex-col gap-2.5">
                {briefingCardCtx ? (
                  <div className="flex flex-col gap-2 mb-1">
                    <div className="inline-flex items-center gap-2.5 font-display text-[10.5px] font-bold tracking-[0.14em] uppercase text-text3">
                      <span aria-hidden="true" className="w-6 h-px bg-border" />
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
                      className={`rounded-xl px-3 py-1.5 text-xs mx-auto mb-1 w-fit tracking-[0.01em] ${
                        orphanRun.status === "cancelled"
                          ? "bg-neutral-soft text-text2"
                          : "bg-warn-soft text-warn-ink"
                      }`}
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
          <div className="border border-warn/40 bg-warn-soft text-warn-ink rounded-2xl px-3 py-2.5 text-[13px]">{streamError}</div>
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

        <div
          className={`work-composer-shell${sending ? " is-working" : ""}`}
        >
          {files.length > 0 ? (
            <div className="flex flex-wrap gap-1.5 px-2 pt-2 pb-1">
              {files.map((file, index) => (
                <div key={`${file.name}-${index}`} className="work-file-chip">
                  <Paperclip size={11} strokeWidth={2} aria-hidden />
                  <span className="max-w-[140px] overflow-hidden text-ellipsis whitespace-nowrap text-text">
                    {file.name}
                  </span>
                  <span className="text-text3 tabular-nums text-[10.5px] tracking-wide">
                    {Math.max(1, Math.round(file.size / 1024))} KB
                  </span>
                  <button
                    type="button"
                    aria-label={`Remove ${file.name}`}
                    onClick={() =>
                      setFiles((prev) => prev.filter((_, fileIndex) => fileIndex !== index))
                    }
                  >
                    <X size={11} strokeWidth={2.25} />
                  </button>
                </div>
              ))}
            </div>
          ) : null}
          <textarea
            ref={textareaRef}
            className="work-input"
            placeholder={sending ? "Working…" : "Send a message…"}
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
            autoComplete="off"
            autoCorrect="on"
            spellCheck
            enterKeyHint="send"
          />
          <div className="flex items-center justify-between gap-2.5 px-1.5 py-1 max-[720px]:px-1">
            <div className="inline-flex items-center gap-2 min-w-0">
              <button
                className="work-icon-btn"
                onClick={() => fileInputRef.current?.click()}
                title="Attach a file"
                aria-label="Attach a file"
                type="button"
                disabled={sending || files.length >= MAX_ATTACHMENTS}
              >
                <Paperclip size={15} strokeWidth={2} />
              </button>
              <span className="work-composer-hint" aria-live="polite">
                {sending ? (
                  <span className="work-composer-pulse">Working</span>
                ) : files.length > 0 ? (
                  <>{files.length} of {MAX_ATTACHMENTS} attached</>
                ) : null}
              </span>
            </div>
            {sending ? (
              <button
                className="work-send-btn is-stop"
                type="button"
                onClick={() => void cancelRun()}
                aria-label="Stop"
              >
                <Square size={13} fill="currentColor" strokeWidth={0} aria-hidden />
                <span>Stop</span>
              </button>
            ) : (
              <button
                className="work-send-btn"
                type="button"
                onClick={() => void sendMessage()}
                disabled={!draft.trim() && files.length === 0}
                aria-label="Send"
              >
                <span>Send</span>
                <ArrowUp size={14} strokeWidth={2.5} aria-hidden />
              </button>
            )}
          </div>
        </div>

        <input
          ref={fileInputRef}
          type="file"
          multiple
          hidden
          accept={ACCEPTED_ATTACHMENT_EXTENSIONS.join(",")}
          onChange={(event) => {
            const picked = Array.from(event.target.files ?? []);
            event.target.value = "";
            if (picked.length === 0) return;
            const accepted: File[] = [];
            const rejections: string[] = [];
            const startingCount = files.length;
            for (const file of picked) {
              if (startingCount + accepted.length >= MAX_ATTACHMENTS) {
                rejections.push(`Max ${MAX_ATTACHMENTS} files per message.`);
                break;
              }
              if (file.type.startsWith("image/")) {
                rejections.push(`"${file.name}": image uploads aren't supported yet.`);
                continue;
              }
              if (file.size > MAX_ATTACHMENT_SIZE) {
                rejections.push(`"${file.name}": over ${Math.round(MAX_ATTACHMENT_SIZE / (1024 * 1024))} MB.`);
                continue;
              }
              if (!ACCEPTED_ATTACHMENT_SUFFIXES.has(fileExtension(file.name))) {
                rejections.push(`"${file.name}": unsupported file type.`);
                continue;
              }
              accepted.push(file);
            }
            if (accepted.length > 0) {
              setFiles((prev) => [...prev, ...accepted].slice(0, MAX_ATTACHMENTS));
            }
            setStreamError(rejections.length > 0 ? rejections.join(" · ") : null);
          }}
        />
      </div>
    </>
  );
}

const EMPTY_PROMPTS: Array<{ label: string; text: string }> = [
  { label: "Top customers", text: "Who are our top 10 customers by revenue this year?" },
  { label: "Revenue trend", text: "How has revenue changed over the last 4 quarters?" },
  { label: "Inventory risk", text: "Which products are below their reorder threshold?" },
];

function EmptyAsk({ onPick }: { onPick: (text: string) => void }) {
  return (
    <div className="flex flex-col gap-[18px] pt-2 pb-1">
      <div className="flex flex-col gap-2 max-w-[620px]">
        <h1 className="font-body text-[22px] font-semibold tracking-[-0.005em] text-text m-0 leading-[1.25]">
          What do you want to know?
        </h1>
        <p className="text-[13.5px] leading-[1.55] text-text2 m-0">
          Ask anything about your business data. I&apos;ll query the database,
          read anything you attach, and answer with charts or tables.
        </p>
      </div>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-2">
        {EMPTY_PROMPTS.map((prompt) => (
          <button
            key={prompt.label}
            type="button"
            className="work-empty-prompt flex flex-col gap-1 px-[13px] py-[11px] bg-white/55 border border-border rounded-xl cursor-pointer text-left font-inherit text-inherit transition-[border-color,background,transform] duration-[180ms]"
            onClick={() => onPick(prompt.text)}
          >
            <span className="text-[10px] font-bold tracking-[0.1em] uppercase text-accent">
              {prompt.label}
            </span>
            <span className="text-[12.5px] leading-[1.4] text-text2">
              {prompt.text}
            </span>
          </button>
        ))}
      </div>
    </div>
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
    <div className="flex items-center justify-between gap-3 border border-border bg-white/80 rounded-2xl px-3 py-2.5 shadow-soft">
      <div className="min-w-0 text-[12.5px] leading-[1.45] text-text2">
        <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-0.5">Memory suggestion</div>
        <div>{item.draftText}</div>
        {pending.length > 1 ? (
          <div className="mt-1 text-text3 text-[11px]">+{pending.length - 1} more</div>
        ) : null}
      </div>
      <div className="inline-flex gap-[7px] flex-wrap justify-end flex-shrink-0 [&_button]:h-[30px] [&_button]:rounded-[10px] [&_button]:border [&_button]:border-border [&_button]:bg-card [&_button]:text-text2 [&_button]:inline-flex [&_button]:items-center [&_button]:justify-center [&_button]:gap-1 [&_button]:px-2 [&_button]:text-[11px] [&_button]:cursor-pointer [&_button]:transition-all [&_button]:duration-200 [&_button:hover]:border-accent [&_button:hover]:text-accent">
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

type ApprovalItem = {
  actionRequestId: string;
  actionKind: string;
  intent: string | null;
  summary: string | null;
  decision: "auto_approved" | "pending_approval";
  result:
    | Extract<WorkEvent, { type: "action_request_result" }>
    | null;
};

type TimelineItem =
  | { kind: "text"; content: string }
  | { kind: "tools"; tools: ToolItem[]; followedByText: boolean }
  | { kind: "approval"; approval: ApprovalItem }
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
  const approvalIndexByRequest = new Map<string, number>();
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
        // A tool_start can arrive twice (e.g. a replayed in-flight run);
        // update the existing row in place rather than emitting a duplicate.
        const existing = toolsById.get(event.id);
        if (existing) {
          existing.name = event.name;
          existing.input = event.input;
          break;
        }
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
      case "action_request_emit": {
        flushTextSegment();
        const approval: ApprovalItem = {
          actionRequestId: event.action_request_id,
          actionKind: event.kind,
          intent: event.intent ?? null,
          summary: event.summary ?? null,
          decision: event.decision,
          result: null,
        };
        approvalIndexByRequest.set(event.action_request_id, items.length);
        items.push({ kind: "approval", approval });
        break;
      }
      case "action_request_result": {
        const idx = approvalIndexByRequest.get(event.action_request_id);
        if (idx == null) break;
        const target = items[idx];
        if (target?.kind !== "approval") break;
        target.approval = { ...target.approval, result: event };
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

function FenceAwareBubble({
  keyPrefix,
  raw,
}: {
  keyPrefix: string;
  raw: string;
}) {
  const text = stripNekoFences(raw);
  const ruleEvent = extractRuleSaveEvent(raw);
  const workflowEvent = extractWorkflowSaveEvent(raw);
  const actionEvents = extractActionRequestEvents(raw);
  return (
    <>
      {text ? (
        <div key={`${keyPrefix}-text`} className="work-bubble-row">
          <div className="work-bubble">
            <div className="work-markdown">
              <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={MARKDOWN_COMPONENTS}>{linkifyWorkspacePaths(text)}</ReactMarkdown>
            </div>
          </div>
        </div>
      ) : null}
      {ruleEvent ? (
        <div key={`${keyPrefix}-rule`} className="flex justify-start mt-1.5 text-left">
          <RuleSavedCard payload={ruleEvent} href="/settings/rules" />
        </div>
      ) : null}
      {workflowEvent ? (
        <div key={`${keyPrefix}-workflow`} className="flex justify-start mt-1.5 text-left">
          <WorkflowSavedCard payload={workflowEvent} href="/workflows" />
        </div>
      ) : null}
      {actionEvents.map((a, i) => (
        <div key={`${keyPrefix}-action-${i}`} className="flex justify-start mt-1.5 text-left">
          <ActionRequestCard payload={a} href="/actions?filter=awaiting" />
        </div>
      ))}
    </>
  );
}

function RunTimeline({
  threadId,
  run,
  events,
  pending,
  fallbackContent,
}: {
  threadId: string;
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
    <div className="work-timeline flex flex-col gap-2.5 mt-1">
      {!hasContent && !pending && fallbackContent.trim() ? (
        <FenceAwareBubble keyPrefix="fallback" raw={fallbackContent} />
      ) : null}

      {items.map((item, index) => {
        if (item.kind === "text") {
          return (
            <FenceAwareBubble
              key={`text-${index}`}
              keyPrefix={`text-${index}`}
              raw={item.content}
            />
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
        if (item.kind === "approval") {
          return (
            <ActionApprovalCard
              key={`approval-${item.approval.actionRequestId}`}
              threadId={threadId}
              runId={run?.id ?? ""}
              approval={item.approval}
            />
          );
        }
        return (
          <div key={`error-${index}`} className="border border-warn/40 bg-warn-soft text-warn-ink rounded-2xl px-3 py-2.5 text-[13px]">
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
      {!pending && run?.error ? <div className="border border-warn/40 bg-warn-soft text-warn-ink rounded-2xl px-3 py-2.5 text-[13px]">{run.error}</div> : null}
    </div>
  );
}

function ActionApprovalCard({
  threadId,
  runId,
  approval,
}: {
  threadId: string;
  runId: string;
  approval: ApprovalItem;
}) {
  const [busy, setBusy] = useState(false);
  const [rejectMode, setRejectMode] = useState(false);
  const [rejectReason, setRejectReason] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);

  const headline =
    approval.intent ??
    approval.summary ??
    `Agent wants to run "${approval.actionKind}".`;
  const pending = approval.decision === "pending_approval" && !approval.result;
  const settled = approval.result !== null;

  async function decide(decision: "approve" | "reject") {
    if (!runId) {
      setLocalError("Run not ready yet — try again in a moment.");
      return;
    }
    setBusy(true);
    setLocalError(null);
    try {
      const res = await fetch(
        `/api/work/threads/${threadId}/runs/${runId}/approve-action`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            actionRequestId: approval.actionRequestId,
            decision,
            ...(decision === "reject" && rejectReason
              ? { rejectionReason: rejectReason }
              : {}),
          }),
        },
      );
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { error?: string };
        setLocalError(
          body.error ?? `Request failed (${res.status} ${res.statusText})`,
        );
      }
    } catch (e) {
      setLocalError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="border border-border bg-card rounded-2xl px-4 py-3.5 text-[13px] flex flex-col gap-2.5">
      <div className="flex items-baseline justify-between gap-3">
        <div className="font-display text-[11px] font-bold uppercase tracking-[0.14em] text-text3">
          Agent says
        </div>
        <code className="font-mono text-[11px] text-text3">
          {approval.actionKind}
        </code>
      </div>
      <div className="text-text leading-[1.45] italic">“{headline}”</div>

      {pending && !rejectMode ? (
        <div className="flex items-center gap-2 pt-1">
          <button
            type="button"
            disabled={busy}
            onClick={() => decide("approve")}
            className="px-3 py-1.5 rounded-[10px] bg-accent text-white font-display font-bold text-[12px] tracking-[-0.01em] hover:bg-[#5a4cd1] disabled:opacity-50 cursor-pointer"
          >
            {busy ? "Approving…" : "Approve"}
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => setRejectMode(true)}
            className="px-3 py-1.5 rounded-[10px] border border-border text-text2 font-medium text-[12px] hover:bg-neutral-soft disabled:opacity-50 cursor-pointer"
          >
            Reject
          </button>
        </div>
      ) : null}

      {pending && rejectMode ? (
        <div className="flex flex-col gap-2 pt-1">
          <input
            type="text"
            placeholder="Reason (optional, shown to the agent)"
            value={rejectReason}
            onChange={(e) => setRejectReason(e.target.value)}
            disabled={busy}
            className="w-full px-2.5 py-1.5 rounded-[8px] border border-border bg-white text-text text-[12px] focus:outline-none focus:border-accent"
          />
          <div className="flex items-center gap-2">
            <button
              type="button"
              disabled={busy}
              onClick={() => decide("reject")}
              className="px-3 py-1.5 rounded-[10px] bg-danger text-white font-display font-bold text-[12px] tracking-[-0.01em] hover:opacity-90 disabled:opacity-50 cursor-pointer"
            >
              {busy ? "Rejecting…" : "Confirm reject"}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                setRejectMode(false);
                setRejectReason("");
              }}
              className="px-3 py-1.5 rounded-[10px] border border-border text-text2 font-medium text-[12px] hover:bg-neutral-soft cursor-pointer"
            >
              Cancel
            </button>
          </div>
        </div>
      ) : null}

      {approval.decision === "auto_approved" && !settled ? (
        <div className="text-text3 text-[12px] flex items-center gap-1.5">
          <Loader2 className="work-status-spin" size={11} />
          Queued — running in the background…
        </div>
      ) : null}

      {settled ? <ActionResultStrip result={approval.result!} /> : null}

      {localError ? (
        <div className="text-danger text-[12px]">{localError}</div>
      ) : null}
    </div>
  );
}

function ActionResultStrip({
  result,
}: {
  result: Extract<WorkEvent, { type: "action_request_result" }>;
}) {
  const tone =
    result.status === "succeeded"
      ? "border-success-mid/40 bg-success-soft text-success-ink"
      : result.status === "rejected"
        ? "border-border bg-neutral-soft text-text2"
        : "border-warn/40 bg-warn-soft text-warn-ink";
  const label =
    result.status === "succeeded"
      ? "Done"
      : result.status === "rejected"
        ? "Rejected"
        : "Failed";
  return (
    <div className={`mt-1 rounded-[10px] border px-3 py-2 text-[12px] ${tone}`}>
      <span className="font-display font-bold uppercase tracking-[0.1em] text-[10px] mr-2">
        {label}
      </span>
      <span>
        {result.status === "rejected"
          ? (result.rejection_reason ?? "Operator rejected the request.")
          : result.status === "failed"
            ? (result.error ?? "The plugin returned an error.")
            : (result.outcome?.externalRef
                ? `ref: ${result.outcome.externalRef}`
                : "The action completed.")}
      </span>
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
  // Collapsed by default and left alone — the running/failed badges on the
  // header carry live progress, so the body never expands on its own. An
  // auto-open while in flight grew the transcript on every tool call and
  // yanked the viewport down; the operator opens it only if they want detail.
  const [open, setOpen] = useState(false);

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

// Back-compat: confirmation cards (workflow/trigger/rule saves) used to be
// emitted as the dashboard `Briefing` root, which renders the 52px display
// greeting — jarring inside the chat timeline. They now emit a `Confirmation`
// component. Run events persisted before that change are frozen as `Briefing`,
// so normalize them here at render time. A real briefing (BriefingCard
// children) is left untouched.
function remapLegacyConfirmations(messages: A2UIMessage[]): A2UIMessage[] {
  return messages.map((message) => {
    if (!("updateComponents" in message)) return message;
    const comps = message.updateComponents.components;
    const isConfirmation =
      comps.some((c) => c.component === "Briefing") &&
      !comps.some((c) => c.component === "BriefingCard");
    if (!isConfirmation) return message;
    return {
      ...message,
      updateComponents: {
        ...message.updateComponents,
        components: comps.map((c) => {
          if (c.component !== "Briefing") return c;
          const { greeting, subtitle, role, isExample, ...rest } = c;
          void role;
          void isExample;
          return {
            ...rest,
            component: "Confirmation",
            label: typeof greeting === "string" ? greeting : "",
            title: typeof subtitle === "string" ? subtitle : "",
          };
        }),
      },
    };
  });
}

function SurfaceBlock({ messages }: { messages: A2UIMessage[] }) {
  const surfaces = useMemo(() => {
    let next = new Map<string, SurfaceState>();
    for (const message of remapLegacyConfirmations(messages)) {
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
  return <div className="flex flex-col gap-2.5 mt-1">{nodes}</div>;
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
  const prefix = text.trim();
  if (files.length === 0) return prefix;
  const lines = files.map((file) => {
    const kb = Math.max(1, Math.round(file.size / 1024));
    return `- ${file.relativePath}  (${file.name}, ${kb} KB)`;
  });
  const header = `I've attached ${files.length === 1 ? "a file" : "files"}:`;
  return prefix ? `${prefix}\n\n${header}\n${lines.join("\n")}` : `${header}\n${lines.join("\n")}`;
}
