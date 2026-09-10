"use client";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import {
  ArrowUp,
  History,
  MessageSquareText,
  PanelRightClose,
  PanelRightOpen,
  Plus,
  Square,
  Trash2,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { confirmDialog } from "@/components/ConfirmModal";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Spinner } from "@/components/ui/spinner";
import type { RecordAskContext } from "@/lib/record-ask";

type AppChatObject = {
  apiName: string;
  label: string;
  pluralLabel: string;
};

type ThreadSummary = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  lastMessageAt: string;
};

type ChatMessage = {
  id: string;
  runId: string | null;
  role: "user" | "assistant";
  content: string;
  createdAt: string;
};

type ChatRun = {
  id: string;
  status: string;
  error: string | null;
};

type ChatEvent = Record<string, unknown> & { type: string };

type ThreadBundle = {
  thread: {
    id: string;
    title: string;
    createdAt: string;
    updatedAt: string;
    lastMessageAt: string;
  };
  runs: ChatRun[];
  messages: ChatMessage[];
  eventsByRun: Record<string, ChatEvent[]>;
};

function displayTitle(value: string): string {
  const title = value.trim();
  return title && !/^untitled thread$/i.test(title)
    ? title
    : "New conversation";
}

function compactDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
  }).format(date);
}

function inFlight(run: ChatRun): boolean {
  return run.status === "queued" || run.status === "running";
}

function actionRequests(events: ChatEvent[]): Array<{
  id: string;
  summary: string;
}> {
  return events.flatMap((event) => {
    if (
      event.type !== "action_request" ||
      event.decision !== "pending_approval" ||
      typeof event.action_request_id !== "string"
    ) {
      return [];
    }
    return [
      {
        id: event.action_request_id,
        summary:
          typeof event.summary === "string" && event.summary.trim()
            ? event.summary
            : "Review the proposed change",
      },
    ];
  });
}

export function AppChatSidebar({
  appId,
  appLabel,
  objects,
}: {
  appId: string;
  appLabel: string;
  objects: AppChatObject[];
}) {
  const pathname = usePathname() ?? "";
  const searchParams = useSearchParams();
  const [collapsed, setCollapsed] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [bundle, setBundle] = useState<ThreadBundle | null>(null);
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const activeThreadRef = useRef<string | null>(null);
  const skipNextThreadLoadRef = useRef<string | null>(null);
  const requestedThreadRef = useRef(searchParams.get("chat"));

  const recordContext = useMemo<RecordAskContext | null>(() => {
    const prefix = `/a/${appId}`;
    if (pathname !== prefix && !pathname.startsWith(`${prefix}/`)) return null;
    const rest = pathname.slice(prefix.length).split("/").filter(Boolean);
    const object = objects.find((candidate) => candidate.apiName === rest[0]);
    if (!object) return null;

    let surface: RecordAskContext["surface"];
    let recordId: string | undefined;
    if (rest.length === 1) {
      surface = "list";
    } else if (rest[1] === "recycle") {
      surface = rest[2] ? "recycle_detail" : "recycle_list";
      recordId = rest[2] ? decodeURIComponent(rest[2]) : undefined;
    } else if (rest[1] === "new" || rest[2] === "edit") {
      return null;
    } else {
      surface = "detail";
      recordId = decodeURIComponent(rest[1]);
    }
    const listSurface = surface === "list" || surface === "recycle_list";
    const search = searchParams.get("q")?.trim();
    return {
      appId,
      appLabel,
      objectApiName: object.apiName,
      objectLabel: listSurface ? object.pluralLabel : object.label,
      surface,
      ...(recordId ? { recordId } : {}),
      ...(search ? { search } : {}),
      ...(surface === "list" && searchParams.get("mine") === "true"
        ? { myRecords: true }
        : {}),
    };
  }, [appId, appLabel, objects, pathname, searchParams]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setCollapsed(
        window.localStorage.getItem("openneko:app-chat:collapsed") === "1",
      );
    }, 0);
    return () => window.clearTimeout(timer);
  }, []);

  useEffect(() => {
    activeThreadRef.current = activeThreadId;
  }, [activeThreadId]);

  useEffect(() => {
    transcriptRef.current?.scrollTo({
      top: transcriptRef.current.scrollHeight,
      behavior: sending ? "smooth" : "auto",
    });
  }, [bundle?.messages, sending]);

  const loadThreads = useCallback(async () => {
    const response = await fetch(
      `/api/a/${encodeURIComponent(appId)}/chat/threads`,
      {
        cache: "no-store",
      },
    );
    if (!response.ok) throw new Error("Could not load app chat history");
    const payload = (await response.json()) as { threads?: ThreadSummary[] };
    const next = payload.threads ?? [];
    setThreads(next);
    return next;
  }, [appId]);

  const loadThread = useCallback(async (threadId: string) => {
    const response = await fetch(`/api/work/threads/${threadId}`, {
      cache: "no-store",
    });
    if (!response.ok) throw new Error("Could not load this conversation");
    const next = (await response.json()) as ThreadBundle;
    if (activeThreadRef.current !== threadId) return null;
    setBundle(next);
    return next;
  }, []);

  const followRun = useCallback(
    (threadId: string, runId: string) => {
      eventSourceRef.current?.close();
      setSending(true);
      setActiveRunId(runId);
      const source = new EventSource(
        `/api/work/threads/${threadId}/runs/${runId}/events`,
      );
      eventSourceRef.current = source;
      source.onmessage = (messageEvent) => {
        let event: ChatEvent;
        try {
          event = JSON.parse(messageEvent.data) as ChatEvent;
        } catch {
          return;
        }
        if (event.type === "hello") return;
        if (
          event.type === "message" &&
          event.role === "assistant" &&
          typeof event.content === "string"
        ) {
          const content = event.content;
          setBundle((current) => {
            if (!current || current.thread.id !== threadId) return current;
            const existing = current.messages.find(
              (candidate) =>
                candidate.runId === runId && candidate.role === "assistant",
            );
            const messages = existing
              ? current.messages.map((candidate) =>
                  candidate === existing
                    ? { ...candidate, content: candidate.content + content }
                    : candidate,
                )
              : [
                  ...current.messages,
                  {
                    id: `stream-${runId}`,
                    runId,
                    role: "assistant" as const,
                    content,
                    createdAt: new Date().toISOString(),
                  },
                ];
            return { ...current, messages };
          });
        }
        setBundle((current) => {
          if (!current || current.thread.id !== threadId) return current;
          return {
            ...current,
            eventsByRun: {
              ...current.eventsByRun,
              [runId]: [...(current.eventsByRun[runId] ?? []), event],
            },
          };
        });
        if (event.type === "error" && typeof event.message === "string") {
          setError(event.message);
        }
        if (event.type === "done") {
          source.close();
          if (eventSourceRef.current === source) eventSourceRef.current = null;
          setSending(false);
          setActiveRunId(null);
          void loadThread(threadId);
          void loadThreads();
        }
      };
    },
    [loadThread, loadThreads],
  );

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      void loadThreads()
        .then((next) => {
          if (!cancelled) {
            const requested = requestedThreadRef.current;
            requestedThreadRef.current = null;
            setActiveThreadId(
              next.find((thread) => thread.id === requested)?.id ??
                next[0]?.id ??
                null,
            );
          }
        })
        .catch((cause) => {
          if (!cancelled)
            setError(
              cause instanceof Error ? cause.message : "Chat is unavailable",
            );
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    });
    return () => {
      cancelled = true;
      eventSourceRef.current?.close();
      eventSourceRef.current = null;
    };
  }, [appId, loadThreads]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      eventSourceRef.current?.close();
      eventSourceRef.current = null;
      setSending(false);
      setActiveRunId(null);
      if (!activeThreadId) {
        setBundle(null);
        return;
      }
      if (skipNextThreadLoadRef.current === activeThreadId) {
        skipNextThreadLoadRef.current = null;
        setLoading(false);
        return;
      }
      setLoading(true);
      setError(null);
      void loadThread(activeThreadId)
        .then((next) => {
          const run = next?.runs.findLast(inFlight);
          if (!run || activeThreadRef.current !== activeThreadId) return;
          setBundle((current) =>
            current
              ? {
                  ...current,
                  messages: current.messages.filter(
                    (message) =>
                      !(
                        message.runId === run.id && message.role === "assistant"
                      ),
                  ),
                  eventsByRun: { ...current.eventsByRun, [run.id]: [] },
                }
              : current,
          );
          followRun(activeThreadId, run.id);
        })
        .catch((cause) =>
          setError(
            cause instanceof Error ? cause.message : "Chat is unavailable",
          ),
        )
        .finally(() => setLoading(false));
    });
    return () => {
      cancelled = true;
    };
  }, [activeThreadId, followRun, loadThread]);

  function toggleCollapsed() {
    const next = !collapsed;
    setCollapsed(next);
    window.localStorage.setItem(
      "openneko:app-chat:collapsed",
      next ? "1" : "0",
    );
  }

  function startNewThread() {
    eventSourceRef.current?.close();
    setActiveThreadId(null);
    setBundle(null);
    setHistoryOpen(false);
    setError(null);
  }

  async function selectThread(threadId: string) {
    setActiveThreadId(threadId);
    setHistoryOpen(false);
  }

  async function deleteThread(thread: ThreadSummary) {
    const confirmed = await confirmDialog({
      title: `Delete “${displayTitle(thread.title)}”?`,
      description:
        "This removes the conversation from this app's chat history.",
      confirmLabel: "Delete",
      destructive: true,
    });
    if (!confirmed) return;
    const response = await fetch(`/api/work/threads/${thread.id}`, {
      method: "DELETE",
    });
    if (!response.ok) return;
    const remaining = threads.filter((candidate) => candidate.id !== thread.id);
    setThreads(remaining);
    if (activeThreadId === thread.id)
      setActiveThreadId(remaining[0]?.id ?? null);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const message = draft.trim();
    if (!message || sending) return;
    setSending(true);
    setError(null);
    setDraft("");
    try {
      let threadId = activeThreadId;
      if (!threadId) {
        const response = await fetch(
          `/api/a/${encodeURIComponent(appId)}/chat/threads`,
          {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({
              title: message.slice(0, 72),
              recordContext,
            }),
          },
        );
        if (!response.ok)
          throw new Error("Could not start an app conversation");
        const payload = (await response.json()) as {
          thread?: { id?: string; title?: string };
        };
        if (!payload.thread?.id)
          throw new Error("App chat did not return a thread");
        threadId = payload.thread.id;
        activeThreadRef.current = threadId;
        skipNextThreadLoadRef.current = threadId;
        setActiveThreadId(threadId);
        setBundle({
          thread: {
            id: threadId,
            title: payload.thread.title ?? message.slice(0, 72),
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
            lastMessageAt: new Date().toISOString(),
          },
          runs: [],
          messages: [],
          eventsByRun: {},
        });
      }

      const response = await fetch(`/api/work/threads/${threadId}/runs`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ message, recordContext }),
      });
      if (!response.ok) {
        const payload = (await response.json().catch(() => ({}))) as {
          error?: string;
        };
        throw new Error(payload.error ?? "Could not send the message");
      }
      const payload = (await response.json()) as { runId: string };
      const now = new Date().toISOString();
      setBundle((current) =>
        current
          ? {
              ...current,
              messages: [
                ...current.messages,
                {
                  id: `user-${payload.runId}`,
                  runId: payload.runId,
                  role: "user",
                  content: message,
                  createdAt: now,
                },
                {
                  id: `assistant-${payload.runId}`,
                  runId: payload.runId,
                  role: "assistant",
                  content: "",
                  createdAt: now,
                },
              ],
              runs: [
                ...current.runs,
                { id: payload.runId, status: "running", error: null },
              ],
              eventsByRun: { ...current.eventsByRun, [payload.runId]: [] },
            }
          : current,
      );
      followRun(threadId, payload.runId);
      void loadThreads();
    } catch (cause) {
      setDraft(message);
      setSending(false);
      setError(
        cause instanceof Error ? cause.message : "Could not send the message",
      );
    }
  }

  async function cancelRun() {
    if (!activeRunId) return;
    await fetch(`/api/work/runs/${activeRunId}/cancel`, { method: "POST" });
  }

  if (collapsed) {
    return (
      <aside
        className="app-chat-sidebar is-collapsed"
        aria-label={`${appLabel} chat, collapsed`}
      >
        <Button
          variant="ghost"
          type="button"
          onClick={toggleCollapsed}
          title="Open app chat"
          aria-label="Open app chat"
        >
          <PanelRightOpen aria-hidden="true" />
        </Button>
        <span className="app-chat-rail-mark" aria-hidden="true">
          <MessageSquareText />
        </span>
        {threads.length > 0 && (
          <span className="app-chat-rail-count">{threads.length}</span>
        )}
      </aside>
    );
  }

  return (
    <aside className="app-chat-sidebar" aria-label={`${appLabel} app chat`}>
      <header className="app-chat-header">
        <span className="app-chat-heading">
          <MessageSquareText aria-hidden="true" />
          <span>
            <strong>{appLabel}</strong>
            <small>App chat</small>
          </span>
        </span>
        <span className="app-chat-header-actions">
          <Button
            variant="ghost"
            type="button"
            onClick={() => setHistoryOpen((open) => !open)}
            aria-pressed={historyOpen}
            title="Conversation history"
          >
            <History aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            type="button"
            onClick={startNewThread}
            title="New conversation"
          >
            <Plus aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            type="button"
            onClick={toggleCollapsed}
            title="Collapse app chat"
          >
            <PanelRightClose aria-hidden="true" />
          </Button>
        </span>
      </header>

      {historyOpen ? (
        <section
          className="app-chat-history"
          aria-label={`${appLabel} conversation history`}
        >
          <div className="app-chat-history-title">
            <span>History</span>
            <Button variant="ghost" type="button" onClick={startNewThread}>
              <Plus aria-hidden="true" /> New
            </Button>
          </div>
          {threads.length === 0 ? (
            <p>No app conversations yet.</p>
          ) : (
            threads.map((thread) => (
              <div
                className={`app-chat-history-row${thread.id === activeThreadId ? " is-active" : ""}`}
                key={thread.id}
              >
                <Button
                  variant="ghost"
                  type="button"
                  onClick={() => void selectThread(thread.id)}
                >
                  <strong>{displayTitle(thread.title)}</strong>
                  <small>{compactDate(thread.lastMessageAt)}</small>
                </Button>
                <Button
                  variant="ghost"
                  type="button"
                  className="app-chat-history-delete"
                  onClick={() => void deleteThread(thread)}
                  title="Delete conversation"
                  aria-label={`Delete ${displayTitle(thread.title)}`}
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              </div>
            ))
          )}
        </section>
      ) : (
        <div className="app-chat-transcript" ref={transcriptRef}>
          {loading ? (
            <div className="app-chat-state">
              <Spinner className="records-spin" aria-hidden="true" /> Loading
              conversation
            </div>
          ) : !bundle?.messages.length ? (
            <div className="app-chat-empty">
              <span>
                <MessageSquareText aria-hidden="true" />
              </span>
              <strong>Ask in the context of {appLabel}</strong>
              <p>
                This conversation stays with the app. You can also bring in
                related records from other apps you can access.
              </p>
            </div>
          ) : (
            bundle.messages.map((message) => {
              const actions = message.runId
                ? actionRequests(bundle.eventsByRun[message.runId] ?? [])
                : [];
              return (
                <article
                  className={`app-chat-message is-${message.role}`}
                  key={message.id}
                >
                  <div>
                    {message.role === "assistant" ? (
                      message.content ? (
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>
                          {message.content}
                        </ReactMarkdown>
                      ) : sending ? (
                        <span className="app-chat-thinking">
                          <i />
                          <i />
                          <i />
                        </span>
                      ) : null
                    ) : (
                      <p>{message.content}</p>
                    )}
                  </div>
                  {actions.map((action) => (
                    <Link
                      href={`/actions/${action.id}`}
                      key={action.id}
                      className="app-chat-action-link"
                    >
                      <span>Approval needed</span>
                      {action.summary}
                    </Link>
                  ))}
                </article>
              );
            })
          )}
        </div>
      )}

      <footer className="app-chat-composer-wrap">
        {recordContext && (
          <span
            className="app-chat-context"
            title={`${recordContext.appLabel} / ${recordContext.objectLabel}`}
          >
            Context: {recordContext.objectLabel}
            {recordContext.recordId ? " record" : ""}
          </span>
        )}
        <form className="app-chat-composer" onSubmit={submit}>
          <Textarea
            value={draft}
            onChange={(event) => {
              setDraft(event.target.value);
              if (error) setError(null);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                event.currentTarget.form?.requestSubmit();
              }
            }}
            placeholder={`Ask about ${appLabel}…`}
            aria-label={`Ask OpenNeko in ${appLabel}`}
            rows={2}
            maxLength={4_000}
          />
          {sending ? (
            <Button
              variant="ghost"
              type="button"
              onClick={() => void cancelRun()}
              aria-label="Stop response"
              title="Stop response"
            >
              <Square aria-hidden="true" />
            </Button>
          ) : (
            <Button
              variant="ghost"
              type="submit"
              disabled={!draft.trim()}
              aria-label="Send message"
            >
              <ArrowUp aria-hidden="true" />
            </Button>
          )}
        </form>
        {error && (
          <p className="app-chat-error" role="alert">
            {error}
          </p>
        )}
      </footer>
    </aside>
  );
}
