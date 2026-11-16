"use client";

import type { RefObject } from "react";
import { withRunEventFormat } from "@/lib/run-event-format";
import { markWorkStartup } from "@/lib/work-startup-timing";
import { isBrowserOnline, reportNetworkOffline, reportNetworkOnline } from "@/spa/pwa";
import { isRunInFlight } from "./work-thread";
import type { ActiveRunStream, StreamResult, ThreadBundle, WorkEvent } from "./work-types";

export const STREAM_RETRY_MAX_MS = 15_000;

function browserOffline(): boolean {
  return typeof navigator !== "undefined" && navigator.onLine === false;
}

/**
 * Follows the event stream of one run until its done event or a close. While
 * the browser is offline it waits for the online event; while the server
 * cannot answer it tries again after a growing delay.
 */
export function followRunEvents({
  activeRunStreamRef,
  threadId,
  runId,
  afterId,
  skipInitialCount = 0,
  onEvent,
}: {
  activeRunStreamRef: RefObject<ActiveRunStream | null>;
  threadId: string;
  runId: string;
  afterId: number;
  skipInitialCount?: number;
  onEvent: (runId: string, event: WorkEvent) => void;
}): Promise<StreamResult> {
  const current = activeRunStreamRef.current;
  if (current?.runId === runId && current.threadId === threadId) {
    return Promise.resolve("closed");
  }

  current?.close();

  return new Promise<StreamResult>((resolve) => {
    let settled = false;
    let es: EventSource | null = null;
    let retryTimer: number | null = null;
    let cursor = afterId;
    let skipped = 0;
    let retryDelayMs = 250;
    const seenEventIds = new Set<number>();

    const clearRetry = () => {
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
        retryTimer = null;
      }
    };

    const settle = (result: StreamResult) => {
      if (settled) return;
      settled = true;
      clearRetry();
      window.removeEventListener("online", onOnline);
      window.removeEventListener("offline", onOffline);
      document.removeEventListener("visibilitychange", onVisibilityChange);
      es?.close();
      es = null;
      const active = activeRunStreamRef.current;
      if (active?.runId === runId && active.threadId === threadId) {
        activeRunStreamRef.current = null;
      }
      resolve(result);
    };

    const checkRunStillInFlight = async () => {
      if (settled || !isBrowserOnline()) return;
      try {
        const res = await fetch(withRunEventFormat(`/api/v1/work/threads/${threadId}`), {
          cache: "no-store",
        });
        if (!res.ok || settled) return;
        const data = (await res.json()) as ThreadBundle;
        if (settled) return;
        const run = data.runs.find((item) => item.id === runId);
        if (run && !isRunInFlight(run)) {
          settle("done");
        }
      } catch {
        // Still offline or unreachable; retry loop will continue.
      }
    };

    const scheduleReconnect = (delayMs: number) => {
      if (settled || retryTimer !== null) return;
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        if (settled) return;
        connect();
      }, delayMs);
    };

    const connect = () => {
      if (settled) return;
      clearRetry();
      es?.close();
      es = null;

      if (browserOffline()) {
        reportNetworkOffline();
        return;
      }

      const params = cursor > 0 ? `?afterId=${cursor}` : "";
      const source = new EventSource(
        withRunEventFormat(`/api/v1/work/threads/${threadId}/runs/${runId}/events${params}`),
      );
      es = source;
      let openedThisAttempt = false;

      source.onopen = () => {
        if (settled || es !== source) return;
        openedThisAttempt = true;
        retryDelayMs = 250;
        reportNetworkOnline();
        markWorkStartup(runId, "streamOpenMs");
      };

      source.onmessage = (msgEvent) => {
        if (settled || es !== source) return;
        reportNetworkOnline();
        const rawId = msgEvent.lastEventId ? Number(msgEvent.lastEventId) : NaN;
        if (Number.isFinite(rawId) && rawId > 0) {
          if (seenEventIds.has(rawId)) return;
          seenEventIds.add(rawId);
          if (rawId > cursor) cursor = rawId;
        }
        let event: WorkEvent;
        try {
          event = JSON.parse(msgEvent.data) as WorkEvent;
        } catch {
          return;
        }
        if (event.type === "hello") return;
        if (skipped < skipInitialCount) {
          skipped += 1;
          if (event.type === "done") {
            markWorkStartup(runId, "doneMs");
            settle("done");
          }
          return;
        }
        onEvent(runId, event);
        markWorkStartup(runId, "firstEventMs");
        if (
          (event.type === "message" &&
            event.role === "assistant" &&
            event.content) ||
          event.type === "surface"
        ) {
          markWorkStartup(runId, "firstOutputMs");
        }
        if (event.type === "done") {
          markWorkStartup(runId, "doneMs");
          settle("done");
        }
      };

      source.onerror = () => {
        if (settled || es !== source) return;
        source.close();
        es = null;
        if (browserOffline()) {
          reportNetworkOffline();
          return;
        }
        if (openedThisAttempt) {
          void checkRunStillInFlight();
        }
        const delay = retryDelayMs;
        retryDelayMs = Math.min(retryDelayMs * 2, STREAM_RETRY_MAX_MS);
        scheduleReconnect(delay);
      };
    };

    const onOnline = () => {
      if (settled) return;
      retryDelayMs = 250;
      connect();
    };

    const onOffline = () => {
      if (settled) return;
      reportNetworkOffline();
      clearRetry();
      es?.close();
      es = null;
    };

    const onVisibilityChange = () => {
      if (settled || document.visibilityState !== "visible") return;
      if (!es || es.readyState === EventSource.CLOSED) {
        retryDelayMs = 250;
        connect();
      }
    };

    activeRunStreamRef.current = {
      threadId,
      runId,
      close: () => settle("closed"),
    };

    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    document.addEventListener("visibilitychange", onVisibilityChange);
    connect();
  });
}
