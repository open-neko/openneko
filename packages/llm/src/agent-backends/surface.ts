import type { AgentSurfaceMessage } from "../agent-backend";

const NEKO_A2UI_FENCE_RE = /```neko_a2ui\s*([\s\S]*?)```/i;

export function extractSurfaceMessages(raw: string): {
  text: string;
  messages: AgentSurfaceMessage[];
} {
  const match = raw.match(NEKO_A2UI_FENCE_RE);
  if (!match) return { text: raw.trim(), messages: [] };
  try {
    const parsed = JSON.parse(match[1].trim());
    const messages = Array.isArray(parsed) ? (parsed as AgentSurfaceMessage[]) : [];
    const text = raw.replace(match[0], "").trim();
    return { text, messages };
  } catch {
    return { text: raw.trim(), messages: [] };
  }
}
