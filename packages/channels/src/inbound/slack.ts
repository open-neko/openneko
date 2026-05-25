import type { IntentEvent } from "@neko/interaction";

type Obj = Record<string, unknown>;

const asObj = (value: unknown): Obj | null =>
  typeof value === "object" && value !== null ? (value as Obj) : null;

const fromAction = (action: Obj): IntentEvent | null => {
  const actionId = typeof action.action_id === "string" ? action.action_id : "";
  const value = typeof action.value === "string" ? action.value : "";
  if (actionId === "approve") return { kind: "decision", decisionRef: value, choice: "approve" };
  if (actionId === "reject") return { kind: "decision", decisionRef: value, choice: "reject" };
  if (actionId.startsWith("select:")) {
    const sep = value.indexOf(":");
    if (sep < 0) return null;
    return { kind: "select", ref: value.slice(0, sep), optionId: value.slice(sep + 1) };
  }
  return null;
};

/** Slack interactivity (`block_actions`) and Events API messages → IntentEvent[]. */
export const parseSlackInbound = (raw: unknown): IntentEvent[] => {
  const payload = asObj(raw);
  if (!payload) return [];

  if (payload.type === "block_actions" && Array.isArray(payload.actions)) {
    const intents: IntentEvent[] = [];
    for (const entry of payload.actions) {
      const action = asObj(entry);
      const intent = action ? fromAction(action) : null;
      if (intent) intents.push(intent);
    }
    return intents;
  }

  if (payload.type === "event_callback") {
    const event = asObj(payload.event);
    if (event?.type === "message" && typeof event.text === "string" && !event.bot_id) {
      const threadRef = typeof event.thread_ts === "string" ? event.thread_ts : undefined;
      return [{ kind: "utterance", text: event.text, threadRef }];
    }
  }
  return [];
};
