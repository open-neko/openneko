import type { MessageRecord, RunRecord, ThreadBundle, WorkEvent } from "./work-types";

export function prependOlder(prev: ThreadBundle, older: ThreadBundle): ThreadBundle {
  const runIds = new Set(prev.runs.map((run) => run.id));
  const messageIds = new Set(prev.messages.map((message) => message.id));
  return {
    ...prev,
    runs: [...older.runs.filter((run) => !runIds.has(run.id)), ...prev.runs],
    messages: [...older.messages.filter((message) => !messageIds.has(message.id)), ...prev.messages],
    eventsByRun: { ...older.eventsByRun, ...prev.eventsByRun },
    hasOlder: older.hasOlder,
  };
}

export function isRunInFlight(run: RunRecord): boolean {
  return run.status === "queued" || run.status === "running";
}

export function latestInFlightRun(runs: RunRecord[]): RunRecord | null {
  for (let i = runs.length - 1; i >= 0; i -= 1) {
    if (isRunInFlight(runs[i])) return runs[i];
  }
  return null;
}

function needsAssistantTimelinePlaceholder(
  run: RunRecord,
  events: WorkEvent[] | undefined,
): boolean {
  return isRunInFlight(run) || Boolean(run.error) || Boolean(events?.length);
}

export function withAssistantTimelinePlaceholders(bundle: ThreadBundle): ThreadBundle {
  const placeholders: MessageRecord[] = [];
  const assistantRunIds = new Set(
    bundle.messages
      .filter((message) => message.role === "assistant" && message.runId)
      .map((message) => message.runId as string),
  );

  for (const run of bundle.runs) {
    if (assistantRunIds.has(run.id)) continue;
    const runEvents = bundle.eventsByRun[run.id];
    if (!needsAssistantTimelinePlaceholder(run, runEvents)) {
      continue;
    }
    const streamedContent = (runEvents ?? [])
      .filter(
        (event): event is Extract<WorkEvent, { type: "message" }> =>
          event.type === "message" && event.role === "assistant",
      )
      .map((event) => event.content)
      .join("");
    placeholders.push({
      id: `assistant-${run.id}`,
      runId: run.id,
      role: "assistant",
      content: streamedContent,
      createdAt: run.createdAt,
    });
  }

  if (placeholders.length === 0) return bundle;

  const messages = [...bundle.messages];
  for (const placeholder of placeholders) {
    // After the run's own question, or at the run's start time when it has none (a routine run).
    const asked = messages.findLastIndex(
      (message) => message.runId === placeholder.runId && message.role === "user",
    );
    const later = messages.findIndex((message) => message.createdAt > placeholder.createdAt);
    const insertAt = asked >= 0 ? asked + 1 : later >= 0 ? later : messages.length;
    messages.splice(insertAt, 0, placeholder);
  }

  return { ...bundle, messages };
}

export function applyStreamEventToBundle(
  bundle: ThreadBundle,
  runId: string,
  event: WorkEvent,
): ThreadBundle {
  const messages =
    event.type === "message" && event.role === "assistant"
      ? bundle.messages.map((message) =>
          message.runId === runId && message.role === "assistant"
            ? { ...message, content: message.content + event.content }
            : message,
        )
      : bundle.messages;
  const currentEvents = bundle.eventsByRun[runId] ?? [];
  const nextEvents = [...currentEvents, event];
  const nextRuns = bundle.runs.map((run) =>
    run.id === runId
      ? {
          ...run,
          status:
            event.type === "done"
              ? String((event.result as { status?: string } | undefined)?.status ?? run.status)
              : event.type === "error"
              ? "failed"
              : run.status,
          error:
            event.type === "error"
              ? event.message
              : runWasCancelled(event)
              ? null
              : run.error,
        }
      : run,
  );
  return {
    ...bundle,
    messages,
    runs: nextRuns,
    eventsByRun: {
      ...bundle.eventsByRun,
      [runId]: nextEvents,
    },
  };
}

export function runWasCancelled(event: { type: string; result?: unknown }): boolean {
  return event.type === "done" && (event.result as { status?: string } | undefined)?.status === "cancelled";
}
