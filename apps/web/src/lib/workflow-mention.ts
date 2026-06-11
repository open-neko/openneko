/**
 * Sentinel that marks the machine-readable workflow-mention block appended to
 * a /work user message. The block maps each @mention the operator inserted to
 * its workflow id so the agent acts on the exact workflow — names can collide
 * or drift, ids don't. The transcript strips it for display; the agent reads
 * the raw content. Mirrors {@link BRIEFING_CARD_SENTINEL}, but as a suffix.
 */
export const WORKFLOW_MENTION_SENTINEL = "::neko-workflow-mentions::";

export type WorkflowMention = { id: string; name: string };

/**
 * Append the mention block to a message body. Returns the body unchanged when
 * there are no mentions, so callers can append unconditionally.
 */
export function appendWorkflowMentionBlock(
  body: string,
  mentions: WorkflowMention[],
): string {
  if (mentions.length === 0) return body;
  const payload = mentions.map((m) => ({ id: m.id, name: m.name }));
  return `${body}\n\n${WORKFLOW_MENTION_SENTINEL}${JSON.stringify(payload)}`;
}

/**
 * Strip the trailing mention block from a message for display/editing. The
 * agent still sees it in the stored content; this is purely cosmetic.
 */
export function stripWorkflowMentionBlock(content: string): string {
  const at = content.indexOf(WORKFLOW_MENTION_SENTINEL);
  if (at === -1) return content;
  return content.slice(0, at).trimEnd();
}
