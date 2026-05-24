import type { InteractionEvent, Projection } from "@neko/interaction";
import { escapeXml, summarizeBody } from "./degrade.js";

export interface VoiceProjectionResult {
  ssml: string;
}

const utterance = (event: InteractionEvent): string => {
  if (event.kind === "converse") return event.text;
  if (event.kind === "inform") {
    const metric = event.metric ? `${event.metric.label} is ${event.metric.value}. ` : "";
    const body = summarizeBody(event.body, "summary");
    return `${event.title}. ${metric}${body}`.trim();
  }
  if (event.kind === "ask") {
    if (event.ask === "approval") return `${event.prompt} Say approve or reject.`;
    if (event.ask === "choice" && event.options?.length) {
      return `${event.prompt} You can say: ${event.options.map((o) => o.label).join(", ")}.`;
    }
    return event.prompt;
  }
  if (event.kind === "resolve") return event.summary;
  return "";
};

/** SSML for an eyes-free substrate. No charts, no images, body trimmed to one clause. */
export const voiceProjection: Projection<VoiceProjectionResult> = (events) => {
  const spoken = events
    .map(utterance)
    .filter((line) => line.length > 0)
    .map(escapeXml)
    .join('<break time="500ms"/> ');
  return { ssml: `<speak>${spoken}</speak>` };
};
