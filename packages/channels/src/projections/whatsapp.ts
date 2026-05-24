import type { InteractionEvent, Projection } from "@neko/interaction";
import { clampChars, summarizeBody } from "./degrade.js";

export interface WhatsappButton {
  id: string;
  title: string;
}

export interface WhatsappProjectionResult {
  body: string;
  buttons?: WhatsappButton[];
}

const askButtons = (event: Extract<InteractionEvent, { kind: "ask" }>): WhatsappButton[] => {
  if (event.ask === "choice" && event.options?.length) {
    return event.options.slice(0, 3).map((option) => ({
      id: `select:${event.decisionRef}:${option.id}`,
      title: option.label.slice(0, 20),
    }));
  }
  if (event.ask === "approval") {
    return [
      { id: `approve:${event.decisionRef}`, title: "Approve" },
      { id: `reject:${event.decisionRef}`, title: "Reject" },
    ];
  }
  return [];
};

/** Text + interactive reply buttons; charts/cards dropped; clamped to the char budget. */
export const whatsappProjection: Projection<WhatsappProjectionResult> = (events, profile) => {
  const parts: string[] = [];
  let buttons: WhatsappButton[] | undefined;
  for (const event of events) {
    if (event.kind === "converse") {
      parts.push(event.text);
    } else if (event.kind === "inform") {
      const body = summarizeBody(event.body, profile.fidelity);
      const metric = event.metric ? `\n${event.metric.label}: ${event.metric.value}` : "";
      parts.push(`*${event.title}*${body ? `\n${body}` : ""}${metric}`);
    } else if (event.kind === "ask") {
      parts.push(event.prompt);
      if (!buttons && profile.interaction.quickReplies) buttons = askButtons(event);
    } else if (event.kind === "resolve") {
      const mark = event.status === "succeeded" ? "✅" : event.status === "rejected" ? "🚫" : "⚠️";
      parts.push(`${mark} ${event.summary}`);
    } else if (event.kind === "offer") {
      parts.push(`📎 ${event.label}`);
    }
  }
  const body = clampChars(parts.join("\n\n"), profile.constraints.maxOutboundChars);
  return buttons?.length ? { body, buttons } : { body };
};
