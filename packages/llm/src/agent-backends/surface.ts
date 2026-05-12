import type { AgentSurfaceMessage } from "../agent-backend";

const NEKO_A2UI_FENCE_RE = /```neko_a2ui\s*([\s\S]*?)```/i;
const JSX_TAG_RE = /<\/?[A-Z][A-Za-z0-9]*\b[^>]*>/g;

// Mirror of isValidA2UIMessage in work/tools.ts so both backends apply the
// same validation before emitting `surface` events.
function isValidA2UIMessage(m: unknown): m is AgentSurfaceMessage {
  if (!m || typeof m !== "object") return false;
  const o = m as Record<string, unknown>;
  if (o.version !== "v0.9") return false;
  return (
    "createSurface" in o ||
    "updateComponents" in o ||
    "updateDataModel" in o ||
    "deleteSurface" in o
  );
}

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
    if (Array.isArray(parsed)) {
      const messages = parsed.filter(isValidA2UIMessage);
      return { text: outsideText, messages };
    }
    // Non-array JSON falls through to synthetic markdown surface.
  } catch {
    // body wasn't JSON — fall through to synthetic markdown fallback
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
