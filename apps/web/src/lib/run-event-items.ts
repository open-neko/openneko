export type RunEventRow = {
  seq: number;
  type: string;
  event: Record<string, unknown> | null;
  createdAt: string;
};

export type RunEventItem =
  | { type: "message-block"; seq: number; content: string }
  | { type: "single"; seq: number; ev: RunEventRow };

export function coalesceRunEvents(events: RunEventRow[]): RunEventItem[] {
  let items: RunEventItem[] = [];
  let current: { seq: number; content: string } | null = null;
  for (const ev of events) {
    if (ev.type === "answer_reset") {
      current = null;
      items = items.filter((item) => item.type !== "message-block");
      continue;
    }
    if (ev.type === "message") {
      const content = (ev.event as { content?: string } | null)?.content ?? "";
      if (current) {
        current.content += content;
      } else {
        current = { seq: ev.seq, content };
      }
      continue;
    }
    if (current) {
      items.push({ type: "message-block", seq: current.seq, content: current.content });
      current = null;
    }
    items.push({ type: "single", seq: ev.seq, ev });
  }
  if (current) {
    items.push({ type: "message-block", seq: current.seq, content: current.content });
  }
  return items;
}
