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
import { extractPolicySaveFence } from "@neko/llm/workflows/fences";
import type { PolicySavePayload } from "@neko/llm/workflows/fence-schemas";
import { fetchAssistantTextFromRun } from "@/lib/run-events-fallback";
import {
  RuleSavedCard,
  extractRuleSaveEvent,
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
  "Describe a rule you want to add. I'll ask a few questions and save it.";

export default function NewPolicyPage() {
  const router = useRouter();
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [threadId, setThreadId] = useState<string | null>(null);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [livePayload, setLivePayload] = useState<Partial<PolicySavePayload>>({});
  const [savedPolicyId, setSavedPolicyId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [streaming, setStreaming] = useState(false);

  const baselineIdsRef = useRef<Set<string> | null>(null);
  const scrollAnchorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    void fetch("/api/policies", { cache: "no-store" })
      .then((r) => r.json())
      .then((data: { policies: { id: string }[] }) => {
        baselineIdsRef.current = new Set(
          (data?.policies ?? []).map((p) => p.id),
        );
      })
      .catch(() => {
        baselineIdsRef.current = new Set();
      });
  }, []);

  useEffect(() => {
    scrollAnchorRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  const checkForSavedPolicy = useCallback(async () => {
    if (savedPolicyId) return;
    if (!baselineIdsRef.current) return;
    try {
      const res = await fetch("/api/policies", { cache: "no-store" });
      const data = (await res.json()) as { policies: { id: string }[] };
      const baseline = baselineIdsRef.current;
      const fresh = (data.policies ?? []).find((p) => !baseline.has(p.id));
      if (fresh) setSavedPolicyId(fresh.id);
    } catch {
      // best-effort
    }
  }, [savedPolicyId]);

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

  useEffect(() => {
    const lastAssistant = [...messages]
      .reverse()
      .find((m) => m.role === "assistant" && m.content);
    if (!lastAssistant) return;
    const res = extractPolicySaveFence(lastAssistant.content);
    if (res.payload) setLivePayload(res.payload);
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
        res = await fetch("/api/policies/builder", {
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
        void checkForSavedPolicy();
        // Recover the live card payload from the seq-ordered DB log in case
        // the streaming chunks arrived out of order.
        void fetchAssistantTextFromRun(data.runId).then((text) => {
          if (!text) return;
          const res = extractPolicySaveFence(text);
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
        if (parsed.type === "done") settle();
      };

      es.onerror = () => {};
    },
    [applyAssistantDelta, checkForSavedPolicy, streaming, threadId],
  );

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void sendMessage(input);
  };

  const cleanedMessages = messages;

  return (
    <>
      <div className="root builder-root">
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="builder-crumb">
          <button
            type="button"
            className="builder-crumb-link"
            onClick={() => router.push("/settings/policies")}
          >
            ← Rules
          </button>
          <span className="builder-crumb-sep">/</span>
          <span>New rule</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="builder-seed">
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
                    m.role === "assistant" ? extractRuleSaveEvent(m.content) : null;
                  const items: React.ReactNode[] = [];
                  if (text || (isTyping && !event)) {
                    items.push(
                      <li
                        key={`${m.id}-msg`}
                        className={`builder-msg builder-msg-${m.role}`}
                      >
                        {text || (
                          <span className="builder-msg-typing">…</span>
                        )}
                      </li>,
                    );
                  }
                  if (event) {
                    items.push(
                      <li key={`${m.id}-event`} className="rule-event-row">
                        <RuleSavedCard payload={event} />
                      </li>,
                    );
                  }
                  return items;
                })}
                <div ref={scrollAnchorRef} />
              </ul>
            )}

            {error && <div className="builder-error">{error}</div>}

            <form className="builder-input-row" onSubmit={onSubmit}>
              <textarea
                className="builder-input"
                placeholder={
                  messages.length === 0
                    ? "Describe the rule…"
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
            <LivePolicyCard
              payload={livePayload}
              saved={Boolean(savedPolicyId)}
              onBack={() => router.push("/settings/policies")}
            />
          </aside>
        </div>
      </div>

      <CreatorCredit />
    </>
  );
}

function LivePolicyCard({
  payload,
  saved,
  onBack,
}: {
  payload: Partial<PolicySavePayload>;
  saved: boolean;
  onBack: () => void;
}) {
  return (
    <div className="builder-card-inner">
      <div className="builder-card-head">
        <div className="builder-card-name">
          {payload.name || "Untitled rule"}
        </div>
        <span
          className={`builder-card-pill builder-card-pill-${saved ? "saved" : "draft"}`}
        >
          {saved ? "saved" : "draft"}
        </span>
      </div>

      <CardField label="Description">
        {payload.description ? (
          <span>{payload.description}</span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Mode">
        {payload.mode ? (
          <span className="builder-card-mono">{payload.mode}</span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Applies to">
        {payload.applies_to_kinds?.length ? (
          <span className="builder-card-mono">
            {payload.applies_to_kinds.join(", ")}
          </span>
        ) : (
          <span className="builder-card-pending">— pending —</span>
        )}
      </CardField>

      <CardField label="Auto-approve">
        {payload.risk_threshold_auto_approve ? (
          <span className="builder-card-mono">
            risk ≤ {payload.risk_threshold_auto_approve}
          </span>
        ) : (
          <span className="builder-card-pending">never</span>
        )}
      </CardField>

      <CardField label="Limits">
        {payload.limits && Object.keys(payload.limits).length > 0 ? (
          <span className="builder-card-mono">
            {Object.entries(payload.limits)
              .map(([k, v]) => `${k}: ${String(v)}`)
              .join(" · ")}
          </span>
        ) : (
          <span className="builder-card-pending">none</span>
        )}
      </CardField>

      {saved && (
        <div className="builder-card-actions">
          <button
            type="button"
            className="builder-card-btn is-primary"
            onClick={onBack}
          >
            Back to rules
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
