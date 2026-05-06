"use client";

import Link from "next/link";
import { useState, useEffect, useRef, useCallback } from "react";
import { useRouter } from "next/navigation";
import { applyMessage, getResolvedComponents } from "@/a2ui/surface";
import type { A2UIMessage, SurfaceState, A2UIComponent } from "@/a2ui/types";
import type { BriefingCardProps } from "@/a2ui/catalog";
import BriefingCard from "@/components/BriefingCard";
import type { BriefingCardData } from "@/components/BriefingCard";
import { ChatBubble, TypingIndicator } from "@/components/ChatSection";
import type { ChatMsg } from "@/components/ChatSection";
import InputBar from "@/components/InputBar";

export default function Dashboard() {
  const router = useRouter();
  const [gateChecked, setGateChecked] = useState(false);
  const [roles, setRoles] = useState<string[]>([]);
  const [role, setRole] = useState("");
  const [gateError, setGateError] = useState<string | null>(null);

  // Onboarding gate: redirect to wizard or processing if no current profile.
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
          // Flag only — wizard fetches the actual message from the status
          // route. Don't pass the message through the URL; that's a toast-
          // spoofing vector.
          router.replace("/onboarding?failed=1");
          return;
        }
        if (status.state === "processing") {
          router.replace("/processing");
          return;
        }
        const seats: string[] = Array.isArray(status.seats) ? status.seats : [];
        setRoles(seats);
        setRole(seats[0] ?? "");
        setGateChecked(true);
      } catch (err) {
        if (cancelled) return;
        setGateError(err instanceof Error ? err.message : "Could not reach server");
        setGateChecked(true);
      }
    })();
    return () => { cancelled = true; };
  }, [router]);
  const [surfaces, setSurfaces] = useState<Map<string, SurfaceState>>(new Map());
  // Chat is stored per-role so an in-flight question on one persona doesn't
  // block input on another, and so polling can deliver answers to the role
  // that asked even after the user switches away.
  const [chatByRole, setChatByRole] = useState<Map<string, ChatMsg[]>>(new Map());
  const [busyRoles, setBusyRoles] = useState<Set<string>>(new Set());
  const [typingRoles, setTypingRoles] = useState<Set<string>>(new Set());

  const [inp, setInp] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const endRef = useRef<HTMLDivElement>(null);

  const msgs = chatByRole.get(role) ?? [];
  const isBusy = busyRoles.has(role);
  const isTyping = typingRoles.has(role);

  const updateRoleMsgs = (r: string, updater: (prev: ChatMsg[]) => ChatMsg[]) => {
    setChatByRole((prev) => {
      const next = new Map(prev);
      next.set(r, updater(prev.get(r) ?? []));
      return next;
    });
  };

  const surfaceId = `briefing-${role.toLowerCase()}`;
  const surface = surfaces.get(surfaceId);

  // Fetch briefing from API when role changes
  const fetchBriefing = useCallback(async (r: string) => {
    setLoading(true);
    try {
      const res = await fetch(`/api/briefing?role=${r}`);
      const messages: A2UIMessage[] = await res.json();

      setSurfaces((prev) => {
        let next = prev;
        for (const msg of messages) {
          next = applyMessage(next, msg);
        }
        return next;
      });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!gateChecked) return;
    if (!role) {
      setLoading(false);
      return;
    }
    fetchBriefing(role);
  }, [role, fetchBriefing, gateChecked]);

  // Extract resolved components from the surface
  const resolved = surface ? getResolvedComponents(surface) : [];
  const rootComp = resolved.find((c) => c.id === "root");
  const briefingComps = resolved.filter((c) => c.component === "BriefingCard");

  // Greeting and subtitle from the resolved Briefing root component
  const greeting = (rootComp as A2UIComponent & { greeting?: string })?.greeting ?? "";
  const subtitle = (rootComp as A2UIComponent & { subtitle?: string })?.subtitle ?? "";
  const isExample = (rootComp as A2UIComponent & { isExample?: boolean })?.isExample === true;

  // Map BriefingCard A2UI components to the shape our React component expects
  const briefingCards: BriefingCardData[] = briefingComps.map((c) => {
    const props = c as unknown as BriefingCardProps & { id: string };
    return {
      id: props.id,
      metricId: props.metricId,
      source: props.source,
      mood: props.mood,
      text: props.text,
      metric: props.metric,
      label: props.label,
      detail: props.detail,
      chart: props.chartType,
      chartData: props.chartData,
    };
  });

  const dismissCard = async (metricId: string) => {
    await fetch("/api/briefing/pin", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ metricId, active: false }),
    });
    fetchBriefing(role);
  };

  const rerunQuestion = (userMsgId: string) => {
    const original = msgs.find((m) => m.type === "user" && m.id === userMsgId);
    if (!original) return;
    void send(original.text, userMsgId);
  };

  const submitEdit = (userMsgId: string, newText: string) => {
    setEditingId(null);
    void send(newText, userMsgId);
  };

  const deleteExchange = (userMsgId: string) => {
    if (editingId === userMsgId) setEditingId(null);
    updateRoleMsgs(role, (prev) => {
      const idx = prev.findIndex((m) => m.type === "user" && m.id === userMsgId);
      if (idx === -1) return prev;
      const removeCount = prev[idx + 1]?.type === "ai" ? 2 : 1;
      const next = [...prev];
      next.splice(idx, removeCount);
      return next;
    });
  };

  // Send a chat message — two-pass flow:
  //   Pass 1 (sync, ~2-5s): classify question → show skeleton card
  //   Pass 2 (async, ~30-60s): worker runs metric agent → poll until done
  // Captures `askedRole` at call time so a mid-poll persona switch routes
  // the answer back to the role that asked, not whichever role is active.
  // If `replaceUserMsgId` is provided, the existing user msg with that id
  // and its AI reply are removed before the new exchange begins — used by
  // edit/rerun to swap a question's card in place.
  const send = async (overrideText?: string, replaceUserMsgId?: string) => {
    const text = (overrideText ?? inp).trim();
    if (!text || busyRoles.has(role)) return;
    const askedRole = role;
    const askedSurfaceId = `briefing-${askedRole.toLowerCase()}`;
    const newUserMsgId = crypto.randomUUID();

    updateRoleMsgs(askedRole, (prev) => {
      const newUserMsg: ChatMsg = { type: "user", id: newUserMsgId, text };
      if (!replaceUserMsgId) return [...prev, newUserMsg];
      const idx = prev.findIndex((m) => m.type === "user" && m.id === replaceUserMsgId);
      if (idx === -1) return [...prev, newUserMsg];
      // Drop the user msg and its AI reply (the next msg, if AI).
      const removeCount = prev[idx + 1]?.type === "ai" ? 2 : 1;
      const next = [...prev];
      next.splice(idx, removeCount, newUserMsg);
      return next;
    });
    if (overrideText === undefined) setInp("");
    setTypingRoles((prev) => new Set(prev).add(askedRole));
    setBusyRoles((prev) => new Set(prev).add(askedRole));

    const clearTyping = () =>
      setTypingRoles((prev) => {
        if (!prev.has(askedRole)) return prev;
        const next = new Set(prev);
        next.delete(askedRole);
        return next;
      });

    try {
      // Pass 1: classify + enqueue
      const res = await fetch("/api/briefing", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ surfaceId: askedSurfaceId, message: text }),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: "request failed" }));
        updateRoleMsgs(askedRole, (p) => [...p, { type: "ai", text: err.error ?? "Something went wrong." }]);
        return;
      }
      const result = await res.json();

      // Demo mode: server returned a fully-formed mock answer. Skip polling.
      if (result.mock) {
        clearTyping();
        const a = result.answer as {
          text: string; metric: string; label: string;
          chartType: string; chartData: Array<{ d: string; v: number; t?: number }>;
        };
        updateRoleMsgs(askedRole, (p) => [...p, {
          type: "ai",
          id: result.chatId,
          text: a.text,
          metric: a.metric,
          label: a.label,
          chartType: a.chartType,
          chartData: a.chartData,
        }]);
        return;
      }

      const { jobId, chatId, skeleton } = result;
      clearTyping();

      // Show skeleton card
      const skeletonMsg: ChatMsg = {
        type: "ai",
        id: chatId,
        text: skeleton.text,
        chartType: skeleton.chartType,
        chartData: skeleton.chartData,
      };
      updateRoleMsgs(askedRole, (p) => [...p, skeletonMsg]);

      // Pass 2: poll for result
      const pollInterval = 3000;
      const maxPolls = 120; // 6 minutes max
      for (let i = 0; i < maxPolls; i++) {
        await new Promise((r) => setTimeout(r, pollInterval));
        try {
          const statusRes = await fetch(`/api/briefing/status?jobId=${jobId}`);
          const statusData = await statusRes.json();

          if (statusData.status === "succeeded" && statusData.payload) {
            const p = statusData.payload;
            updateRoleMsgs(askedRole, (prev) =>
              prev.map((m) =>
                m.id === chatId
                  ? {
                      ...m,
                      metricId: statusData.metricId,
                      text: p.insightText ?? skeleton.text,
                      metric: p.headlineMetric,
                      label: p.headlineLabel,
                      chartType: p.chartType ?? skeleton.chartType,
                      chartData: p.chartData ?? [],
                    }
                  : m,
              ),
            );
            break;
          }
          if (statusData.status === "failed") {
            updateRoleMsgs(askedRole, (prev) =>
              prev.map((m) =>
                m.id === chatId
                  ? { ...m, text: `Error: ${statusData.error ?? "unknown"}` }
                  : m,
              ),
            );
            break;
          }
          // Still running — keep polling
        } catch {
          // Network error on poll — keep trying
        }
      }
    } catch {
      updateRoleMsgs(askedRole, (p) => [...p, { type: "ai", text: "Something went wrong." }]);
    } finally {
      clearTyping();
      setBusyRoles((prev) => {
        if (!prev.has(askedRole)) return prev;
        const next = new Set(prev);
        next.delete(askedRole);
        return next;
      });
    }
  };

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [msgs, isTyping]);

  // Lazy-load this role's chat from localStorage the first time it becomes
  // the active role. Once loaded into chatByRole, the in-memory Map is the
  // source of truth — no need to reload on subsequent switches.
  useEffect(() => {
    if (!role || chatByRole.has(role)) return;
    let parsed: ChatMsg[] = [];
    try {
      const stored = localStorage.getItem(`app_chat:${role}`);
      if (stored) {
        // Backfill ids on legacy user msgs that predate the rerun/edit
        // feature so the hover actions can identify them.
        parsed = (JSON.parse(stored) as ChatMsg[]).map((m) =>
          m.type === "user" && !m.id ? { ...m, id: crypto.randomUUID() } : m,
        );
      }
    } catch {}
    setChatByRole((prev) => {
      if (prev.has(role)) return prev;
      const next = new Map(prev);
      next.set(role, parsed);
      return next;
    });
  }, [role, chatByRole]);

  // Persist each loaded role's chat (last 6). Switching roles doesn't mutate
  // any other role's entry, so there's no cross-persona overwrite.
  useEffect(() => {
    for (const [r, m] of chatByRole) {
      try { localStorage.setItem(`app_chat:${r}`, JSON.stringify(m.slice(-6))); } catch {}
    }
  }, [chatByRole]);

  const now = new Date();
  const ds = now.toLocaleDateString("en-IN", { weekday: "long", month: "long", day: "numeric" });

  return (
    <>
      <div className="root">
        <div className="topbar">
          <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
            <div className="pills">
              {roles.map((k) => (
                <button key={k} className={`pill${role === k ? " on" : ""}`} onClick={() => setRole(k)}>
                  {k}
                </button>
              ))}
            </div>
            <Link href="/settings" className="settings-link">
              Settings
            </Link>
          </div>
          <div className="topbar-date">{ds}</div>
        </div>

        {gateError ? (
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
            <div style={{ marginBottom: 8, color: "var(--text2)" }}>
              Can&apos;t reach the database right now.
            </div>
            <div style={{ fontSize: 13 }}>
              The briefing will load once the connection is back.
            </div>
            <button
              onClick={() => { setGateError(null); setGateChecked(false); setLoading(true); window.location.reload(); }}
              style={{ marginTop: 16, padding: "6px 14px", fontSize: 13, cursor: "pointer" }}
            >
              Retry
            </button>
          </div>
        ) : loading ? (
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
            Loading briefing...
          </div>
        ) : (
          <>
            {isExample && (
              <Link
                href="/onboarding"
                className="example-banner"
                style={{ animation: "fadeUp 0.5s ease both" }}
              >
                Example metrics — set up your own →
              </Link>
            )}
            <div className="greet" style={{ animation: "fadeUp 0.5s ease both" }}>{greeting}</div>
            <div className="greet-sub" style={{ animation: "fadeUp 0.5s ease 0.05s both" }}>{subtitle}</div>

            <div style={{ marginBottom: 24 }}>
              <div className="label">Today&apos;s Briefing</div>
              {briefingCards.map((ins, i) => (
                <BriefingCard
                  key={ins.id}
                  ins={ins}
                  index={i}
                  onDismiss={() => dismissCard(ins.metricId)}
                />
              ))}
            </div>
          </>
        )}

        {msgs.length > 0 && (
          <>
            <div className="divider" />
            <div className="label">Conversation</div>
            {msgs.map((m, i) => (
              <ChatBubble
                key={m.id ?? i}
                msg={m}
                isEditing={!!m.id && editingId === m.id}
                busy={isBusy}
                onStartEdit={(id) => setEditingId(id)}
                onCancelEdit={() => setEditingId(null)}
                onSubmitEdit={submitEdit}
                onRerun={rerunQuestion}
                onDelete={deleteExchange}
              />
            ))}
            {isTyping && <TypingIndicator />}
            <div ref={endRef} />
          </>
        )}
      </div>

      <InputBar value={inp} onChange={setInp} onSend={send} disabled={isBusy} />
    </>
  );
}
