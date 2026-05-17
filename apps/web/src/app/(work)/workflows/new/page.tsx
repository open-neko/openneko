"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useRouter } from "next/navigation";
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

const SEED_HINT =
  "Tell me what this workflow should watch for. I'll ask a few questions, then save it.";

export default function NewWorkflowPage() {
  const router = useRouter();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [threadId, setThreadId] = useState<string | null>(null);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [livePayload, setLivePayload] = useState<Partial<WorkflowSavePayload>>(
    {},
  );
  const [savedWorkflowId, setSavedWorkflowId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [streaming, setStreaming] = useState(false);

  // Baseline of workflow ids that already exist when this page loads — so we
  // can detect when *this* run produced a new one.
  const baselineIdsRef = useRef<Set<string> | null>(null);
  const scrollAnchorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    void fetch("/api/workflows", { cache: "no-store" })
      .then((r) => r.json())
      .then((data: { workflows: { id: string }[] }) => {
        baselineIdsRef.current = new Set(
          (data?.workflows ?? []).map((w) => w.id),
        );
      })
      .catch(() => {
        baselineIdsRef.current = new Set();
      });
  }, []);

  useEffect(() => {
    scrollAnchorRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  const checkForSavedWorkflow = useCallback(async () => {
    if (savedWorkflowId) return;
    if (!baselineIdsRef.current) return;
    try {
      const res = await fetch("/api/workflows", { cache: "no-store" });
      const data = (await res.json()) as { workflows: { id: string }[] };
      const baseline = baselineIdsRef.current;
      const fresh = (data.workflows ?? []).find((w) => !baseline.has(w.id));
      if (fresh) {
        setSavedWorkflowId(fresh.id);
      }
    } catch {
      // best-effort poll; ignore network blips
    }
  }, [savedWorkflowId]);

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

  // Whenever the visible assistant text changes, attempt to extract a fence
  // payload so the right pane can fill in live.
  useEffect(() => {
    const lastAssistant = [...messages]
      .reverse()
      .find((m) => m.role === "assistant" && m.content);
    if (!lastAssistant) return;
    const res = extractWorkflowSaveFence(lastAssistant.content);
    if (res.payload) {
      setLivePayload(res.payload);
    }
  }, [messages]);

  const sendMessage = useCallback(
    async (text: string) => {
      const trimmed = text.trim();
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

      let res: Response;
      try {
        res = await fetch("/api/workflows/builder", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            message: trimmed,
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
        void checkForSavedWorkflow();
        // Recover the live card payload from the seq-ordered DB log in case
        // the streaming chunks arrived out of order and broke the live parse.
        void fetchAssistantTextFromRun(data.runId).then((text) => {
          if (!text) return;
          const res = extractWorkflowSaveFence(text);
          if (res.payload) setLivePayload(res.payload);
        });
      };

      es.onmessage = (evt) => {
        let parsed: StreamEvent;
        try {
          parsed = JSON.parse(evt.data) as StreamEvent;
        } catch {
          return;
        }
        if (parsed.type === "message" && "content" in parsed && "role" in parsed && parsed.role === "assistant") {
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
        // EventSource auto-reconnects; settle if connection is permanently lost
        // by listening for the done event above.
      };
    },
    [applyAssistantDelta, checkForSavedWorkflow, streaming, threadId],
  );

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void sendMessage(input);
  };

  const cleanedMessages = messages;

  return (
    <div className="builder-root">
      <div className="flex items-center gap-2 text-[12.5px] text-text3 mt-1 mb-[22px] font-mono">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-inherit p-0 hover:text-accent"
            onClick={() => router.push("/workflows")}
          >
            ← Workflows
          </button>
          <span className="opacity-50">/</span>
          <span>New workflow</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="flex-1 grid place-items-center text-text3 text-sm py-[60px] px-6 leading-[1.55] [&>*]:max-w-80 [&>*]:text-center">
                <p>{SEED_HINT}</p>
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
                          href={
                            savedWorkflowId
                              ? `/workflows?id=${savedWorkflowId}`
                              : "/workflows"
                          }
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
                placeholder={
                  messages.length === 0
                    ? "Describe what to watch for…"
                    : "Reply…"
                }
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
            <LiveWorkflowCard
              payload={livePayload}
              saved={Boolean(savedWorkflowId)}
              onRunTest={() => {
                if (!savedWorkflowId) return;
                void fetch(`/api/workflows/${savedWorkflowId}/runs`, {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({}),
                })
                  .then((r) => r.json())
                  .then((d) => {
                    if (d.workflowRunId) {
                      router.push(`/runs/${d.workflowRunId}`);
                    } else if (d.threadId) {
                      router.push(`/work/${d.threadId}`);
                    } else {
                      router.push(`/workflows?id=${savedWorkflowId}`);
                    }
                  })
                  .catch(() => {
                    router.push(`/workflows?id=${savedWorkflowId}`);
                  });
              }}
              onOpenInDrawer={() => {
                if (!savedWorkflowId) return;
                router.push(`/workflows?id=${savedWorkflowId}`);
              }}
            />
          </aside>
        </div>
    </div>
  );
}

function LiveWorkflowCard({
  payload,
  saved,
  onRunTest,
  onOpenInDrawer,
}: {
  payload: Partial<WorkflowSavePayload>;
  saved: boolean;
  onRunTest: () => void;
  onOpenInDrawer: () => void;
}) {
  const stateLabel = saved ? "saved" : "draft";

  return (
    <div className="bg-card border border-border rounded-2xl p-[18px]">
      <div className="flex items-center justify-between gap-2 pb-3 mb-3.5 border-b border-border">
        <div className="font-display text-base font-extrabold tracking-[-0.01em] text-text min-w-0 overflow-hidden text-ellipsis whitespace-nowrap">
          {payload.name || "Untitled workflow"}
        </div>
        <span className={cn(
          "text-[10px] font-bold tracking-[0.12em] uppercase px-2.5 py-[3px] rounded-full flex-shrink-0",
          saved ? "bg-success-soft text-success-mid" : "bg-neutral text-text3",
        )}>
          {stateLabel}
        </span>
      </div>

      <CardField label="Name">
        {payload.name ? (
          <span>{payload.name}</span>
        ) : (
          <span className="text-text3 italic">— pending —</span>
        )}
      </CardField>

      <CardField label="Goal">
        {payload.goal ? (
          <span>{payload.goal}</span>
        ) : (
          <span className="text-text3 italic">— pending —</span>
        )}
      </CardField>

      <CardField label="Steps">
        {payload.steps?.length ? (
          <ol className="m-0 pl-[18px] [&>li]:mb-[3px]">
            {payload.steps.map((s, i) => (
              <li key={s.id ?? i}>{s.description}</li>
            ))}
          </ol>
        ) : (
          <span className="text-text3 italic">— pending —</span>
        )}
      </CardField>

      <CardField label="Schedule">
        {payload.triggers?.cron ? (
          <span className="font-mono text-xs text-text2">
            {payload.triggers.cron} ({payload.triggers.timezone ?? "UTC"})
          </span>
        ) : (
          <span className="text-text3 italic">— pending —</span>
        )}
      </CardField>

      {saved && (
        <div className="mt-4 pt-3 border-t border-border flex flex-col gap-2">
          <button
            type="button"
            className="px-3 py-2 rounded-lg border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:bg-[#5a4cd1] hover:border-[#5a4cd1]"
            onClick={onRunTest}
          >
            Run a test now
          </button>
          <button
            type="button"
            className="px-3 py-2 rounded-lg border border-border bg-card text-text font-body text-[13px] font-semibold cursor-pointer hover:border-text3"
            onClick={onOpenInDrawer}
          >
            Open in workflows
          </button>
        </div>
      )}
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
