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
import { cn } from "@/lib/cn";
import { extractWorkflowSaveFence } from "@neko/llm/workflows/fences";
import type { WorkflowSavePayload } from "@neko/llm/workflows/fence-schemas";
import { fetchAssistantTextFromRun } from "@/lib/run-events-fallback";
import {
  WorkflowSavedCard,
  extractWorkflowSaveEvent,
  stripNekoFences,
} from "@/components/RuleChatBubble";

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

  const cleanedMessages = messages;

  if (loadError) {
    return (
      <div className="builder-root">
        <div className="text-danger text-[12.5px] mt-0 mb-2">Couldn&apos;t load workflow: {loadError}</div>
      </div>
    );
  }

  if (!workflow) {
    return (
      <div className="builder-root">
        <div className="flex-1 grid place-items-center text-text3 text-sm py-[60px] px-6 leading-[1.55] [&>*]:max-w-80 [&>*]:text-center">Loading workflow…</div>
      </div>
    );
  }

  return (
    <div className="builder-root">
      <div className="flex items-center gap-2 text-[12.5px] text-text3 mt-1 mb-[22px] font-mono">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-inherit p-0 hover:text-accent"
            onClick={() => router.push(`/workflows?id=${workflowId}`)}
          >
            ← {workflow.name}
          </button>
          <span className="opacity-50">/</span>
          <span>Edit</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="flex-1 grid place-items-center text-text3 text-sm py-[60px] px-6 leading-[1.55] [&>*]:max-w-80 [&>*]:text-center">
                <p>
                  Tell me what you want to change about{" "}
                  <strong>{workflow.name}</strong>.
                </p>
              </div>
            ) : (
              <ul className="builder-msgs">
                {cleanedMessages.flatMap((m) => {
                  const isTyping =
                    m.role === "assistant" && activeRunId === m.runId;
                  const text =
                    m.role === "assistant" ? stripNekoFences(m.content) : m.content;
                  const event =
                    m.role === "assistant" ? extractWorkflowSaveEvent(m.content) : null;
                  const items: React.ReactNode[] = [];
                  if (text || (isTyping && !event)) {
                    items.push(
                      <li
                        key={`${m.id}-msg`}
                        className={`builder-msg builder-msg-${m.role}`}
                      >
                        {text || (
                          <span className="text-text3">…</span>
                        )}
                      </li>,
                    );
                  }
                  if (event) {
                    items.push(
                      <li key={`${m.id}-event`} className="rule-event-row">
                        <WorkflowSavedCard
                          payload={event}
                          href={`/workflows?id=${workflowId}`}
                        />
                      </li>,
                    );
                  }
                  return items;
                })}
                <div ref={scrollAnchorRef} />
              </ul>
            )}

            {error && <div className="text-danger text-[12.5px] mt-0 mb-2">{error}</div>}

            <form className="flex gap-2 items-stretch border-t border-border pt-3" onSubmit={onSubmit}>
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
                className="bg-accent text-white border-0 rounded-xl px-4 font-body text-[13px] font-semibold cursor-pointer self-stretch disabled:bg-neutral disabled:text-text3 disabled:cursor-not-allowed"
                disabled={!input.trim() || streaming}
              >
                {streaming ? "…" : "Send"}
              </button>
            </form>
          </section>

          <aside className="builder-card">
            <div className="bg-card border border-border rounded-2xl p-[18px]">
              <div className="flex items-center justify-between gap-2 pb-3 mb-3.5 border-b border-border">
                <div className="font-display text-base font-extrabold tracking-[-0.01em] text-text min-w-0 overflow-hidden text-ellipsis whitespace-nowrap">
                  {livePayload.name || workflow.name}
                </div>
                <span
                  className={cn(
                    "text-[10px] font-bold tracking-[0.12em] uppercase px-2.5 py-[3px] rounded-full flex-shrink-0",
                    savedAtLeastOnce ? "bg-success-soft text-success-mid" : "bg-neutral text-text3",
                  )}
                >
                  {savedAtLeastOnce ? "saved" : "current"}
                </span>
              </div>

              <CardField label="Goal">
                {livePayload.goal ?? workflow.goal ?? "—"}
              </CardField>

              <CardField label="Steps">
                <ol className="m-0 pl-[18px] [&>li]:mb-[3px]">
                  {(livePayload.steps ?? workflow.steps).map((s, i) => (
                    <li key={s.id ?? i}>{s.description}</li>
                  ))}
                </ol>
              </CardField>

              <CardField label="Schedule">
                {livePayload.triggers?.cron || workflow.cron ? (
                  <span className="font-mono text-xs text-text2">
                    {livePayload.triggers?.cron ?? workflow.cron} (
                    {livePayload.triggers?.timezone ??
                      workflow.cronTimezone ??
                      "UTC"}
                    )
                  </span>
                ) : (
                  <span className="text-text3 italic">manual only</span>
                )}
              </CardField>

              {savedAtLeastOnce && (
                <div className="mt-4 pt-3 border-t border-border flex flex-col gap-2">
                  <button
                    type="button"
                    className="px-3 py-2 rounded-lg border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:bg-[#5a4cd1] hover:border-[#5a4cd1]"
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
    <div className="mb-3 last-of-type:mb-0">
      <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-1">{label}</div>
      <div className="text-[13px] text-text leading-[1.5]">{children}</div>
    </div>
  );
}
