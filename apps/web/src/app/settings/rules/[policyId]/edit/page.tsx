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

type PolicyDetail = {
  policy: {
    id: string;
    name: string;
    description: string;
    appliesToKinds: string[];
    appliesToScopes: string[];
    mode: string;
    riskThresholdAutoApprove: string | null;
    limits: Record<string, unknown>;
    enabled: boolean;
  };
};

export default function EditPolicyPage() {
  const params = useParams<{ policyId: string }>();
  const policyId = params?.policyId;
  const router = useRouter();

  const [policy, setPolicy] = useState<PolicyDetail["policy"] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [threadId, setThreadId] = useState<string | null>(null);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [livePayload, setLivePayload] = useState<Partial<PolicySavePayload>>({});
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAtLeastOnce, setSavedAtLeastOnce] = useState(false);

  const sentContextRef = useRef(false);
  const scrollAnchorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!policyId) return;
    let cancelled = false;
    void fetch(`/api/policies/${policyId}`, { cache: "no-store" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<PolicyDetail>;
      })
      .then((data) => {
        if (cancelled) return;
        setPolicy(data.policy);
        setLivePayload({
          name: data.policy.name,
          description: data.policy.description,
          applies_to_kinds: data.policy.appliesToKinds,
          applies_to_scopes: data.policy.appliesToScopes as (
            | "internal"
            | "external"
          )[],
          mode: data.policy.mode as PolicySavePayload["mode"],
          risk_threshold_auto_approve:
            (data.policy.riskThresholdAutoApprove as PolicySavePayload["risk_threshold_auto_approve"]) ??
            undefined,
          limits: data.policy.limits,
          enabled: data.policy.enabled,
        });
      })
      .catch((err) => {
        if (cancelled) return;
        setLoadError(
          err instanceof Error ? err.message : "Failed to load policy",
        );
      });
    return () => {
      cancelled = true;
    };
  }, [policyId]);

  useEffect(() => {
    scrollAnchorRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    const lastAssistant = [...messages]
      .reverse()
      .find((m) => m.role === "assistant" && m.content);
    if (!lastAssistant) return;
    const res = extractPolicySaveFence(lastAssistant.content);
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

  const buildServerMessage = useCallback(
    (typed: string): string => {
      if (sentContextRef.current || !policy) return typed;
      sentContextRef.current = true;
      return [
        `You are editing an existing OpenNeko action policy. The current saved`,
        `definition is below. When you produce a neko_policy_save fence, keep`,
        `the same "name" — the runtime applies the fence as an update by id,`,
        `but the name should remain stable so operator references don't drift.`,
        "",
        "Current policy:",
        "```json",
        JSON.stringify(
          {
            name: policy.name,
            description: policy.description,
            applies_to_kinds: policy.appliesToKinds,
            applies_to_scopes: policy.appliesToScopes,
            mode: policy.mode,
            risk_threshold_auto_approve: policy.riskThresholdAutoApprove,
            limits: policy.limits,
            enabled: policy.enabled,
          },
          null,
          2,
        ),
        "```",
        "",
        "My change request:",
        typed,
      ].join("\n");
    },
    [policy],
  );

  const sendMessage = useCallback(
    async (typed: string) => {
      const trimmed = typed.trim();
      if (!trimmed || streaming || !policyId) return;
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
        res = await fetch("/api/policies/builder", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            message: serverMessage,
            editingPolicyId: policyId,
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
          const res = extractPolicySaveFence(text);
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
        if (parsed.type === "done") settle();
      };

      es.onerror = () => {};
    },
    [applyAssistantDelta, buildServerMessage, streaming, threadId, policyId],
  );

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void sendMessage(input);
  };

  const cleanedMessages = messages;

  if (loadError) {
    return (
      <div className="root builder-root">
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>
        <div className="text-danger text-[12.5px] mt-0 mb-2">Couldn&apos;t load rule: {loadError}</div>
      </div>
    );
  }

  if (!policy) {
    return (
      <div className="root builder-root">
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>
        <div className="flex-1 grid place-items-center text-text3 text-sm py-[60px] px-6 leading-[1.55] [&>*]:max-w-80 [&>*]:text-center">
          <p>Loading rule…</p>
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="root builder-root">
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="flex items-center gap-2 text-[12.5px] text-text3 mt-1 mb-[22px] font-mono">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-inherit p-0 hover:text-accent"
            onClick={() => router.push("/settings/rules")}
          >
            ← Rules
          </button>
          <span className="opacity-50">/</span>
          <span>{policy.name}</span>
          <span className="opacity-50">/</span>
          <span>Edit</span>
        </div>

        <div className="builder-layout">
          <section className="builder-chat">
            {messages.length === 0 ? (
              <div className="flex-1 grid place-items-center text-text3 text-sm py-[60px] px-6 leading-[1.55] [&>*]:max-w-80 [&>*]:text-center">
                <p>
                  Tell me what you want to change about{" "}
                  <strong>{policy.name}</strong>.
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
                    m.role === "assistant" ? extractRuleSaveEvent(m.content) : null;
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
                        <RuleSavedCard
                          payload={event}
                          href={`/settings/rules/${policyId}/edit`}
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
                  {livePayload.name || policy.name}
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

              <CardField label="Description">
                {livePayload.description ?? policy.description ?? "—"}
              </CardField>

              <CardField label="Mode">
                <span className="font-mono text-xs text-text2">
                  {livePayload.mode ?? policy.mode}
                </span>
              </CardField>

              <CardField label="Applies to">
                <span className="font-mono text-xs text-text2">
                  {(livePayload.applies_to_kinds ?? policy.appliesToKinds).join(
                    ", ",
                  ) || "any action"}
                </span>
              </CardField>

              <CardField label="Auto-approve">
                {(livePayload.risk_threshold_auto_approve ?? policy.riskThresholdAutoApprove) ? (
                  <span className="font-mono text-xs text-text2">
                    risk ≤{" "}
                    {livePayload.risk_threshold_auto_approve ??
                      policy.riskThresholdAutoApprove}
                  </span>
                ) : (
                  <span className="text-text3 italic">never</span>
                )}
              </CardField>

              <CardField label="Limits">
                {Object.keys(livePayload.limits ?? policy.limits).length > 0 ? (
                  <span className="font-mono text-xs text-text2">
                    {Object.entries(livePayload.limits ?? policy.limits)
                      .map(([k, v]) => `${k}: ${String(v)}`)
                      .join(" · ")}
                  </span>
                ) : (
                  <span className="text-text3 italic">none</span>
                )}
              </CardField>

              {savedAtLeastOnce && (
                <div className="mt-4 pt-3 border-t border-border flex flex-col gap-2">
                  <button
                    type="button"
                    className="px-3 py-2 rounded-lg border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:bg-[#5a4cd1] hover:border-[#5a4cd1]"
                    onClick={() => router.push("/settings/rules")}
                  >
                    Done — back to policies
                  </button>
                </div>
              )}
            </div>
          </aside>
        </div>
      </div>

      <CreatorCredit />
    </>
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
