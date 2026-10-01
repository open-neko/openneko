import { pool } from "@neko/db";
import type { AgentEvent } from "../agent-backend";

export const PROVISIONAL_RUN_CHANNEL = "work_run_progress";
type ProvisionalAnswer = Extract<AgentEvent, {type: "provisional_answer"}>;
export type ProvisionalRunNotification = {orgId: string; runId: string; event: ProvisionalAnswer};

/** PostgreSQL NOTIFY has an 8 KiB payload limit. Split by code point, not UTF-16 unit. */
export function provisionalNotifications(orgId: string, runId: string, event: ProvisionalAnswer): string[] {
  const payloads: string[] = [];
  let text = "";
  let bytes = 0;
  const push = () => {
    if (!text) return;
    payloads.push(JSON.stringify({orgId, runId, event: {...event, text}}));
    text = "";
    bytes = 0;
  };
  for (const character of event.text) {
    const size = Buffer.byteLength(character, "utf8");
    if (bytes + size > 2048) push();
    text += character;
    bytes += size;
  }
  push();
  return payloads;
}

export function parseProvisionalNotification(raw: string): ProvisionalRunNotification | undefined {
  if (Buffer.byteLength(raw, "utf8") > 4096) return undefined;
  try {
    const value = JSON.parse(raw) as Partial<ProvisionalRunNotification>;
    const event = value.event;
    if (typeof value.orgId !== "string" || !value.orgId || typeof value.runId !== "string" || !value.runId ||
        !event || event.type !== "provisional_answer" || event.index !== 0 ||
        !Number.isSafeInteger(event.version) || event.version < 0 ||
        typeof event.text !== "string" || !event.text || Buffer.byteLength(event.text, "utf8") > 2048)
      return undefined;
    return value as ProvisionalRunNotification;
  } catch {
    return undefined;
  }
}

/** Best-effort live projection; callers must never treat delivery as a receipt. */
export async function publishProvisionalRunAnswer(orgId: string, runId: string, event: ProvisionalAnswer): Promise<void> {
  for (const payload of provisionalNotifications(orgId, runId, event))
    await pool().query("select pg_notify($1, $2)", [PROVISIONAL_RUN_CHANNEL, payload]);
}
