"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useParams, useRouter } from "next/navigation";
import { extractWorkflowSaveFence } from "@neko/llm/workflows/fences";
import type { WorkflowSavePayload } from "@neko/llm/workflows/fence-schemas";
import { fetchAssistantTextFromRun } from "@/lib/run-events-fallback";

type ChatMessage = {
  id: string;
  role: "user" | "assistant";
  content: string;
  runId?: string;
};

type StreamEvent =
  | { type: "hello"; runId?: string; threadId?: string }
  | { type: "message"; role: "assistant"; content: string }
  | { type: "status"; message: string }
  | { type: "error"; message: string }
  | { type: "done"; result?: { status?: string } }
  | { type: string };

type WorkflowDetail = {
  workflow: {
    id: string;
    name: string;
    description: string;
    goal: string;
    systemPromptOverlay?: string;
    enabled: boolean;
    status: string;
    steps: { id: string; description: string }[];
    cron: string | null;
    cronTimezone: string;
    cronEnabled: boolean;
  };
};

export default function EditWorkflowPage() {
  const params = useParams<{ workflowId: string }>();
  const workflowId = params?.workflowId;
  const router = useRouter();

  const [workflow, setWorkflow] = useState<
    WorkflowDetail["workflow"] | null
  >(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [threadId, setThreadId] = useState<string | null>(null);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [livePayload, setLivePayload] = useState<Partial<WorkflowSavePayload>>({});
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAtLeastOnce, setSavedAtLeastOnce] = useState(false);

  const sentContextRef = useRef(false);
  const scrollAnchorRef = useRef<HTMLDivElement | null>(null);

  // Load the existing definition so the right pane reflects current state.
  useEffect(() => {
    if (!workflowId) return;
    let cancelled = false;
    void fetch(`/api/workflows/${workflowId}`, { cache: "no-store" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<WorkflowDetail>;
      })
      .then((data) => {
        if (cancelled) return;
        setWorkflow(data.workflow);
        setLivePayload({
          name: data.workflow.name,
          description: data.workflow.description,
          goal: data.workflow.goal,
          systemPromptOverlay: data.workflow.systemPromptOverlay,
          steps: data.workflow.steps,
          triggers: data.workflow.cron
            ? {
                cron: data.workflow.cron,
                timezone: data.workflow.cronTimezone,
                enabled: data.workflow.cronEnabled,
              }
            : undefined,
        });
      })
      .catch((err) => {
        if (cancelled) return;
        setLoadError(
          err instanceof Error ? err.message : "Failed to load workflow",
        );
      });
    return () => {
      cancelled = true;
    };
  }, [workflowId]);

  useEffect(() => {
    scrollAnchorRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  // Whenever assistant text changes, re-parse for fence to update card.
  useEffect(() => {
    const lastAssistant = [...messages]
      .reverse()
      .find((m) => m.role === "assistant" && m.content);
    if (!lastAssistant) return;
    const res = extractWorkflowSaveFence(lastAssistant.content);
    if (res.payload) {
      setLivePayload(res.payload);
      setSavedAtLeastOnce(true);
    }
  }, [messages]);

  const applyAssistantDelta = useCallback(
    (runId: string, delta: string) => {
      setMessages((prev) =>
        prev.map((m) =>
          m.role === "assistant" && m.runId === runId
            ? { ...m, content: m.content + delta }
            : m,
        ),
      );
    },
    [],
  );

  // Build the server-side message. The first user submission is wrapped
  // with a context block so the agent knows what it's editing — the visible
  // chat only shows the user's typed text.
  const buildServerMessage = useCallback(
    (typed: string): string => {
      if (sentContextRef.current || !workflow) return typed;
      sentContextRef.current = true;
      const ctx = {
        name: workflow.name,
        description: workflow.description,
        goal: workflow.goal,
        systemPromptOverlay: workflow.systemPromptOverlay,
        steps: workflow.steps,
        triggers: workflow.cron
          ? {
              cron: workflow.cron,
              timezone: workflow.cronTimezone,
              enabled: workflow.cronEnabled,
            }
          : null,
      };
      return [
        `You are editing an existing OpenNeko workflow. The current saved`,
        `definition is below. When you produce a neko_workflow_save fence,`,
        `keep the same "name" so the save upserts the existing row — do not`,
        `rename it unless I explicitly ask.`,
        "",
        "Current definition:",
        "```json",
        JSON.stringify(ctx, null, 2),
        "```",
        "",
        "My change request:",
        typed,
      ].join("\n");
    },
    [workflow],
  );

  const sendMessage = useCallback(
    async (typed: string) => {
      const trimmed = typed.trim();
      if (!trimmed || streaming) return;
      setError(null);
      setInput("");

      const userMsg: ChatMessage = {
        id: `u-${Date.now()}`,
        role: "user",
        content: trimmed,
      };
      setMessages((prev) => [...prev, userMsg]);
      setStreaming(true);

      const serverMessage = buildServerMessage(trimmed);

      let res: Response;
      try {
        res = await fetch("/api/workflows/builder", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            message: serverMessage,
            ...(threadId ? { threadId } : {}),
          }),
        });
      } catch (err) {
        setStreaming(false);
        setError(err instanceof Error ? err.message : "Network error");
        return;
      }

      if (!res.ok) {
        setStreaming(false);
        const data = await res.json().catch(() => ({}));
        setError(data.error ?? `HTTP ${res.status}`);
        return;
      }

      const data = (await res.json()) as { threadId: string; runId: string };
      setThreadId(data.threadId);
      setActiveRunId(data.runId);

      const placeholderId = `a-${data.runId}`;
      setMessages((prev) => [
        ...prev,
        {
          id: placeholderId,
          role: "assistant",
          content: "",
          runId: data.runId,
        },
      ]);

      const es = new EventSource(
        `/api/work/threads/${data.threadId}/runs/${data.runId}/events`,
      );

      const settle = () => {
        es.close();
        setStreaming(false);
        setActiveRunId(null);
        // Recover the live card payload from the seq-ordered DB log in case
        // the streaming chunks arrived out of order.
        void fetchAssistantTextFromRun(data.runId).then((text) => {
          if (!text) return;
          const res = extractWorkflowSaveFence(text);
          if (res.payload) {
            setLivePayload(res.payload);
            setSavedAtLeastOnce(true);
          }
        });
      };

      es.onmessage = (evt) => {
        let parsed: StreamEvent;
        try {
          parsed = JSON.parse(evt.data) as StreamEvent;
        } catch {
          return;
        }
        if (
          parsed.type === "message" &&
          "content" in parsed &&
          "role" in parsed &&
          parsed.role === "assistant"
        ) {
          applyAssistantDelta(data.runId, parsed.content);
          return;
        }
        if (parsed.type === "error" && "message" in parsed) {
          setError(parsed.message);
          return;
        }
        if (parsed.type === "done") {
          settle();
        }
      };

      es.onerror = () => {
        // EventSource auto-reconnects; settle on done event above.
      };
    },
    [applyAssistantDelta, buildServerMessage, streaming, threadId],
  );

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void sendMessage(input);
  };

  const cleanedMessages = useMemo(() => {
    return messages.map((m) => {
      if (m.role !== "assistant") return m;
      const parsed = extractWorkflowSaveFence(m.content);
      return { ...m, content: parsed.text.trim() || m.content };
    });
  }, [messages]);

  if (loadError) {
    return (
      <div className="builder-root">
        <div className="builder-error">Couldn&apos;t load workflow: {loadError}</div>
      </div>
    );
  }

  if (!workflow) {
    return (
      <div className="builder-root">
        <div className="builder-seed">Loading workflow…</div>
      </div>
    );
  }

  return (
    <div className="builder-root">
      <div className="builder-crumb">
          <button
            type="button"
            className="builder-crumb-link"
            onClick={() => router.push(`/workflows?id=${workflowId}`)}
          >
            ← {workflow.name}
          </button>
          <span className="builder-crumb-sep">/</span>
          <span>Edit</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="builder-seed">
                <p>
                  Tell me what you want to change about{" "}
                  <strong>{workflow.name}</strong>.
                </p>
              </div>
            ) : (
              <ul className="builder-msgs">
                {cleanedMessages.map((m) => (
                  <li key={m.id} className={`builder-msg builder-msg-${m.role}`}>
                    {m.content ||
                      (m.role === "assistant" && activeRunId === m.runId ? (
                        <span className="builder-msg-typing">…</span>
                      ) : null)}
                  </li>
                ))}
                <div ref={scrollAnchorRef} />
              </ul>
            )}

            {error && <div className="builder-error">{error}</div>}

            <form className="builder-input-row" onSubmit={onSubmit}>
              <textarea
                className="builder-input"
                placeholder="What should change?"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void sendMessage(input);
                  }
                }}
                disabled={streaming}
                rows={2}
              />
              <button
                type="submit"
                className="builder-send"
                disabled={!input.trim() || streaming}
              >
                {streaming ? "…" : "Send"}
              </button>
            </form>
          </section>

          <aside className="builder-card">
            <div className="builder-card-inner">
              <div className="builder-card-head">
                <div className="builder-card-name">
                  {livePayload.name || workflow.name}
                </div>
                <span
                  className={`builder-card-pill builder-card-pill-${
                    savedAtLeastOnce ? "saved" : "draft"
                  }`}
                >
                  {savedAtLeastOnce ? "saved" : "current"}
                </span>
              </div>

              <CardField label="Goal">
                {livePayload.goal ?? workflow.goal ?? "—"}
              </CardField>

              <CardField label="Steps">
                <ol className="builder-card-steps">
                  {(livePayload.steps ?? workflow.steps).map((s, i) => (
                    <li key={s.id ?? i}>{s.description}</li>
                  ))}
                </ol>
              </CardField>

              <CardField label="Schedule">
                {livePayload.triggers?.cron || workflow.cron ? (
                  <span className="builder-card-mono">
                    {livePayload.triggers?.cron ?? workflow.cron} (
                    {livePayload.triggers?.timezone ??
                      workflow.cronTimezone ??
                      "UTC"}
                    )
                  </span>
                ) : (
                  <span className="builder-card-pending">manual only</span>
                )}
              </CardField>

              {savedAtLeastOnce && (
                <div className="builder-card-actions">
                  <button
                    type="button"
                    className="builder-card-btn is-primary"
                    onClick={() =>
                      router.push(`/workflows?id=${workflowId}`)
                    }
                  >
                    Done — back to workflow
                  </button>
                </div>
              )}
            </div>
          </aside>
        </div>
    </div>
  );
}

function CardField({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="builder-card-field">
      <div className="builder-card-label">{label}</div>
      <div className="builder-card-value">{children}</div>
    </div>
  );
}
