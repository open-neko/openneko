"use client";

import Link from "next/link";
import { useState, useEffect, useRef, useCallback, useMemo } from "react";
import { useRouter } from "next/navigation";
import { applyMessage, getResolvedComponents } from "@/a2ui/surface";
import type { A2UIMessage, SurfaceState, A2UIComponent } from "@/a2ui/types";
import type { BriefingCardProps } from "@/a2ui/catalog";
import BriefingCard from "@/components/BriefingCard";
import type { BriefingCardData } from "@/components/BriefingCard";
import { ChatBubble, TypingIndicator } from "@/components/ChatSection";
import type { ChatMsg } from "@/components/ChatSection";
import InputBar from "@/components/InputBar";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";

/**
 * What we persist to localStorage per chat message — identity references
 * only, no volatile card data. AI cards are rehydrated from the server
 * via /api/briefing/by-metric so a stale browser entry can never desync
 * from the DB. User messages keep their text since the prompt itself
 * isn't on the server.
 */
type StoredChatMsg =
  | { id?: string; type: "user"; text: string }
  | { id?: string; type: "ai"; metricId: string };

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
          router.replace("/business-profile");
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

  // Fetch briefing from API. `silent=true` skips the loading toggle so
  // background polls (pending-card refresh, retry, dismiss) don't unmount
  // the entire briefing+chat tree by flipping the page-level loading
  // ternary. Only the initial role-change fetch should show "Loading…".
  const fetchBriefing = useCallback(async (r: string, silent = false) => {
    if (!silent) setLoading(true);
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
      if (!silent) setLoading(false);
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

  // Track whether the current role's surface has any cards still pending
  // (no snapshot yet, refresh job not yet succeeded). Computed below from
  // resolved components; used by the polling effect that follows.
  const hasPendingCards = useMemo(() => {
    if (!surface) return false;
    const comps = getResolvedComponents(surface).filter(
      (c) => c.component === "BriefingCard",
    );
    return comps.some((c) => {
      const p = c as unknown as BriefingCardProps;
      return p.state === "pending";
    });
  }, [surface]);

  // While any card is pending, re-fetch /api/briefing every 30s so the
  // dashboard reflects newly-completed metric_refresh jobs without the
  // user reloading. Without this, the briefing API result is fetched
  // once on role-change (the effect above) and never again — which
  // means even successful snapshots stay invisible until reload. 30s
  // is comfortable: each metric_refresh takes 30–90s, so we'll catch
  // freshly-landed snapshots within one cycle without burning fetches.
  useEffect(() => {
    if (!hasPendingCards || !role) return;
    const id = setInterval(() => {
      fetchBriefing(role, true);
    }, 30_000);
    return () => clearInterval(id);
  }, [hasPendingCards, role, fetchBriefing]);

  // Cross-role progress tracker for the metric_refresh fan-out. When the
  // user lands on / immediately after onboarding, bootstrap_metrics_build
  // has just enqueued N refresh jobs; we surface "X of Y" as a banner so
  // the otherwise-quiet dashboard isn't a black box.
  const [metricsProgress, setMetricsProgress] = useState<
    { total: number; completed: number; failed: number } | null
  >(null);
  useEffect(() => {
    if (!gateChecked) return;
    let cancelled = false;
    let stopped = false;
    const tick = async () => {
      if (cancelled) return;
      try {
        const res = await fetch("/api/onboarding/status");
        const status = await res.json();
        if (cancelled) return;
        if (status.metricsProgress) {
          // Avoid setState on every poll when the counts haven't moved —
          // a fresh object identity would re-render the dashboard tree
          // and replay the briefing animations even though nothing
          // changed visually.
          setMetricsProgress((prev) => {
            const next = status.metricsProgress;
            if (
              prev &&
              prev.total === next.total &&
              prev.completed === next.completed &&
              prev.failed === next.failed
            ) {
              return prev;
            }
            return next;
          });
          // Once all jobs settle, stop polling.
          const settled =
            status.metricsProgress.completed + status.metricsProgress.failed;
          if (settled >= status.metricsProgress.total) {
            stopped = true;
          }
        } else {
          setMetricsProgress((prev) => (prev === null ? prev : null));
          stopped = true;
        }
      } catch {
        // ignore — try again next tick
      }
    };
    tick();
    const id = setInterval(() => {
      if (stopped) {
        clearInterval(id);
        return;
      }
      tick();
    }, 10_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [gateChecked]);

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
      state: props.state,
      error: props.error,
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
    fetchBriefing(role, true);
  };

  const retryCard = async (metricId: string) => {
    await fetch("/api/briefing/retry", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ metricId }),
    });
    // Force a refetch immediately so the card re-skeletons; the polling
    // effect below will keep refreshing while the new run is pending.
    fetchBriefing(role, true);
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
          text: string; metric: string; label: string; mood: string; detail: string;
          chartType: string; chartData: Array<{ d: string; v: number; t?: number }>;
        };
        updateRoleMsgs(askedRole, (p) => [...p, {
          type: "ai",
          id: result.chatId,
          text: a.text,
          card: {
            id: result.chatId,
            metricId: result.chatId,
            source: "chat",
            state: "ok" as const,
            mood: a.mood,
            text: a.text,
            metric: a.metric,
            label: a.label,
            detail: a.detail,
            chart: a.chartType,
            chartData: a.chartData,
          },
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

      // Pass 2: poll for result. No max-polls cap — the worker queue can
      // sit behind a slow run for 5+ minutes when pg-boss workers are
      // saturated, and giving up before the job lands leaves the user's
      // chat bubble visibly stuck on the skeleton even though the
      // metric_snapshot has actually been written. Backoff: 3s for the
      // first ~minute, then 10s after that to keep the network quiet
      // while still picking up the answer within one cycle.
      let polls = 0;
      // eslint-disable-next-line no-constant-condition
      while (true) {
        const interval = polls < 20 ? 3000 : 10_000;
        await new Promise((r) => setTimeout(r, interval));
        polls++;
        try {
          const statusRes = await fetch(`/api/briefing/status?jobId=${jobId}`);
          const statusData = await statusRes.json();

          if (statusData.status === "succeeded" && statusData.payload) {
            const p = statusData.payload;
            // Build a BriefingCard-shaped result so the chat bubble
            // renders the same card chrome the dashboard does (mood +
            // kpi/chart + expandable detail). Concatenating insight +
            // detail mirrors what /api/briefing GET does for dashboard
            // cards (see briefing/route.ts:dbInsights).
            const card = {
              id: chatId,
              metricId: statusData.metricId,
              source: statusData.source ?? "chat",
              state: "ok" as const,
              mood: p.mood ?? "watch",
              text: statusData.title ?? skeleton.text,
              metric: p.headlineMetric ?? "",
              label: p.headlineLabel ?? "",
              detail: [p.insightText, p.detailText].filter(Boolean).join(" "),
              chart: p.chartType ?? skeleton.chartType,
              chartData: p.chartData ?? [],
            };
            updateRoleMsgs(askedRole, (prev) =>
              prev.map((m) =>
                m.id === chatId
                  ? {
                      ...m,
                      metricId: statusData.metricId,
                      card,
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
  // the active role. localStorage stores ONLY identity references — id,
  // type, user text (or AI metricId). Volatile card data (mood, headline,
  // chart) is fetched fresh from the server via /api/briefing/by-metric
  // so a stale browser entry can never desync from the DB. AI messages
  // without a metricId (transient errors, in-flight skeletons that
  // didn't land before the user reloaded) are dropped on rehydrate.
  useEffect(() => {
    if (!role || chatByRole.has(role)) return;
    let stored: StoredChatMsg[] = [];
    try {
      const raw = localStorage.getItem(`app_chat:${role}`);
      if (raw) stored = JSON.parse(raw) as StoredChatMsg[];
    } catch {}

    const aiNeedingHydration = stored.filter(
      (m): m is StoredChatMsg & { metricId: string } =>
        m.type === "ai" && typeof m.metricId === "string",
    );
    // Reserve the role's slot synchronously so concurrent renders don't
    // re-trigger this effect while the fetches are in flight.
    setChatByRole((prev) => {
      if (prev.has(role)) return prev;
      const next = new Map(prev);
      next.set(
        role,
        stored
          // Drop AI messages with no metricId — they were transient
          // (network errors, in-flight skeletons that never resolved).
          .filter((m) => m.type !== "ai" || m.metricId)
          .map((m): ChatMsg => {
            if (m.type === "user") {
              return {
                type: "user",
                id: m.id ?? crypto.randomUUID(),
                text: m.text ?? "",
              };
            }
            // AI placeholder until /api/briefing/by-metric resolves.
            return {
              type: "ai",
              id: m.id ?? crypto.randomUUID(),
              metricId: m.metricId,
              text: "Loading…",
            };
          }),
      );
      return next;
    });

    if (aiNeedingHydration.length === 0) return;
    let cancelled = false;
    void Promise.all(
      aiNeedingHydration.map(async (m) => {
        try {
          const res = await fetch(`/api/briefing/by-metric?metricId=${m.metricId}`);
          if (!res.ok) return null;
          const body = (await res.json()) as {
            metricId: string;
            title: string | null;
            source: string | null;
            payload: {
              mood?: string;
              headlineMetric?: string;
              headlineLabel?: string;
              insightText?: string;
              detailText?: string;
              chartType?: string;
              chartData?: Array<{ d: string; v: number; t?: number }>;
            } | null;
          };
          return { msgId: m.id, body };
        } catch {
          return null;
        }
      }),
    ).then((resolved) => {
      if (cancelled) return;
      const byMsgId = new Map<string, (typeof resolved)[number]>();
      for (const r of resolved) {
        if (r && r.msgId) byMsgId.set(r.msgId, r);
      }
      updateRoleMsgs(role, (prev) =>
        prev.map((m) => {
          if (m.type !== "ai" || !m.id) return m;
          const r = byMsgId.get(m.id);
          if (!r || !r.body) return m;
          const p = r.body.payload;
          if (!p) {
            // Snapshot not landed yet (job still running) — keep skeleton.
            return { ...m, text: "Fetching…" };
          }
          return {
            ...m,
            text: p.insightText ?? "",
            card: {
              id: m.id,
              metricId: r.body.metricId,
              source: r.body.source ?? "chat",
              state: "ok" as const,
              mood: p.mood ?? "watch",
              text: r.body.title ?? p.insightText ?? "",
              metric: p.headlineMetric ?? "",
              label: p.headlineLabel ?? "",
              detail: [p.insightText, p.detailText].filter(Boolean).join(" "),
              chart: p.chartType ?? "kpi",
              chartData: p.chartData ?? [],
            },
          };
        }),
      );
    });

    return () => { cancelled = true; };
  }, [role, chatByRole]);

  // Persist each loaded role's chat (last 6) as identity-only references.
  // Volatile card data is excluded by design — the rehydrate path fetches
  // fresh data from the server via /api/briefing/by-metric.
  useEffect(() => {
    for (const [r, msgs] of chatByRole) {
      const stored: StoredChatMsg[] = [];
      for (const m of msgs.slice(-6)) {
        if (m.type === "user") {
          stored.push({ id: m.id, type: "user", text: m.text });
        } else if (m.metricId) {
          stored.push({ id: m.id, type: "ai", metricId: m.metricId });
        }
        // AI without metricId = transient (network error, in-flight
        // skeleton); not worth persisting.
      }
      try {
        localStorage.setItem(`app_chat:${r}`, JSON.stringify(stored));
      } catch {}
    }
  }, [chatByRole]);

  // Render the formatted date only after mount. toLocaleDateString depends
  // on the runtime's ICU data, which can differ between the Node.js server
  // and the user's browser — leading to a hydration mismatch if the two
  // emit different strings (e.g. "Wed" vs "Wednesday"). Empty initial value
  // → server and first client render agree → safe; useEffect populates the
  // real string after hydration. Short weekday + long month keeps it
  // compact enough to share a row with the nav links on narrow viewports.
  const [ds, setDs] = useState("");
  useEffect(() => {
    setDs(
      new Date().toLocaleDateString("en-IN", {
        weekday: "short",
        month: "long",
        day: "numeric",
      }),
    );
  }, []);

  return (
    <>
      <div className="root">
        <AppHeader>
          <div className="pills">
            {roles.map((k) => (
              <button key={k} className={`pill${role === k ? " on" : ""}`} onClick={() => setRole(k)}>
                {k}
              </button>
            ))}
          </div>
        </AppHeader>

        {/* Secondary nav strip — full row beneath the AppHeader so the
            brand on the right doesn't crowd this group. Stays on one
            line at any pill count. */}
        <div className="dash-meta">
          {gateChecked && !gateError ? (
            <Link href="/business-profile" className="settings-link">
              Business Profile
            </Link>
          ) : (
            <span className="settings-link is-disabled" aria-disabled="true">
              Business Profile
            </span>
          )}
          <Link href="/settings" className="settings-link">
            Settings
          </Link>
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

            {metricsProgress &&
              metricsProgress.completed + metricsProgress.failed < metricsProgress.total && (
                <div
                  role="status"
                  style={{
                    marginTop: 16,
                    padding: "10px 14px",
                    borderRadius: 10,
                    border: "1px solid var(--border)",
                    background: "var(--accent-soft)",
                    color: "var(--accent)",
                    fontSize: 13,
                    display: "inline-block",
                  }}
                >
                  Building your briefing — {metricsProgress.completed + metricsProgress.failed} of {metricsProgress.total} cards complete
                </div>
              )}

            <div style={{ marginBottom: 24 }}>
              <div className="label">Today&apos;s Briefing</div>
              {briefingCards.map((ins, i) => (
                <BriefingCard
                  key={ins.id}
                  ins={ins}
                  index={i}
                  onDismiss={() => dismissCard(ins.metricId)}
                  onRetry={retryCard}
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
      <CreatorCredit />
    </>
  );
}
