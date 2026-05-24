import type { IntentEvent } from "@neko/interaction";
import { intentFromButtonId } from "./button-id.js";

type Obj = Record<string, unknown>;

const asObj = (value: unknown): Obj | null =>
  typeof value === "object" && value !== null ? (value as Obj) : null;

const fromMessage = (message: Obj): IntentEvent | null => {
  if (message.type === "text") {
    const text = asObj(message.text)?.body;
    return typeof text === "string" ? { kind: "utterance", text } : null;
  }
  if (message.type === "interactive") {
    const reply = asObj(asObj(message.interactive)?.button_reply);
    const id = reply?.id;
    return typeof id === "string" ? intentFromButtonId(id) : null;
  }
  if (message.type === "button") {
    const payload = asObj(message.button)?.payload;
    return typeof payload === "string" ? intentFromButtonId(payload) : null;
  }
  return null;
};

/** WhatsApp Cloud API webhook → IntentEvent[]. */
export const parseWhatsappInbound = (raw: unknown): IntentEvent[] => {
  const payload = asObj(raw);
  if (!payload || !Array.isArray(payload.entry)) return [];
  const intents: IntentEvent[] = [];
  for (const entryValue of payload.entry) {
    const changes = asObj(entryValue)?.changes;
    if (!Array.isArray(changes)) continue;
    for (const changeValue of changes) {
      const messages = asObj(asObj(changeValue)?.value)?.messages;
      if (!Array.isArray(messages)) continue;
      for (const messageValue of messages) {
        const message = asObj(messageValue);
        const intent = message ? fromMessage(message) : null;
        if (intent) intents.push(intent);
      }
    }
  }
  return intents;
};
