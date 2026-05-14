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
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
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

  const cleanedMessages = useMemo(() => {
    return messages.map((m) => {
      if (m.role !== "assistant") return m;
      // Strip the fence block from the visible chat — the right pane shows it.
      const parsed = extractWorkflowSaveFence(m.content);
      return { ...m, content: parsed.text.trim() || m.content };
    });
  }, [messages]);

  return (
    <>
      <div className="root builder-root">
        <AppHeader>
          <SectionNav current="workflows" />
        </AppHeader>

        <div className="builder-crumb">
          <button
            type="button"
            className="builder-crumb-link"
            onClick={() => router.push("/workflows")}
          >
            ← Workflows
          </button>
          <span className="builder-crumb-sep">/</span>
          <span>New workflow</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="builder-seed">
                <p>{SEED_HINT}</p>
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
                className="builder-send"
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

      <CreatorCredit />
    </>
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
    <div className="builder-card-inner">
      <div className="builder-card-head">
        <div className="builder-card-name">
          {payload.name || "Untitled workflow"}
        </div>
        <span className={`builder-card-pill builder-card-pill-${stateLabel}`}>
          {stateLabel}
        </span>
      </div>

      <CardField label="Name">
        {payload.name ? (
          <span>{payload.name}</span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Goal">
        {payload.goal ? (
          <span>{payload.goal}</span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Steps">
        {payload.steps?.length ? (
          <ol className="builder-card-steps">
            {payload.steps.map((s, i) => (
              <li key={s.id ?? i}>{s.description}</li>
            ))}
          </ol>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Schedule">
        {payload.triggers?.cron ? (
          <span className="builder-card-mono">
            {payload.triggers.cron} ({payload.triggers.timezone ?? "UTC"})
          </span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      {saved && (
        <div className="builder-card-actions">
          <button
            type="button"
            className="builder-card-btn is-primary"
            onClick={onRunTest}
          >
            Run a test now
          </button>
          <button
            type="button"
            className="builder-card-btn"
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
    <div className="builder-card-field">
      <div className="builder-card-label">{label}</div>
      <div className="builder-card-value">{children}</div>
    </div>
  );
}
