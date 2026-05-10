import type { AgentSurfaceMessage } from "../agent-backend";

const NEKO_A2UI_FENCE_RE = /```neko_a2ui\s*([\s\S]*?)```/i;
const JSX_TAG_RE = /<\/?[A-Z][A-Za-z0-9]*\b[^>]*>/g;

export function extractSurfaceMessages(raw: string): {
  text: string;
  messages: AgentSurfaceMessage[];
} {
  const match = raw.match(NEKO_A2UI_FENCE_RE);
  if (!match) return { text: raw.trim(), messages: [] };
  const outsideText = raw.replace(match[0], "").trim();
  const body = match[1].trim();
  try {
    const parsed = JSON.parse(body);
    const messages = Array.isArray(parsed) ? (parsed as AgentSurfaceMessage[]) : [];
    if (messages.length > 0) return { text: outsideText, messages };
  } catch {
    // fall through to synthetic fallback
  }
  const proseFallback = body.replace(JSX_TAG_RE, "").trim();
  if (!proseFallback) return { text: outsideText, messages: [] };
  return {
    text: outsideText,
    messages: synthesizeMarkdownSurface(proseFallback),
  };
}

function synthesizeMarkdownSurface(text: string): AgentSurfaceMessage[] {
  const surfaceId = "fallback";
  return [
    {
      version: "v0.9",
      createSurface: { surfaceId, catalogId: "urn:app:catalog:briefing:v1" },
    } as AgentSurfaceMessage,
    {
      version: "v0.9",
      updateComponents: {
        surfaceId,
        components: [{ id: "md", component: "Markdown", text }],
      },
    } as AgentSurfaceMessage,
  ];
}
