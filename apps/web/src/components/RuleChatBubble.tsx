"use client";

import { extractPolicySaveFence } from "@neko/llm/workflows/fences";
import { POLICY_SAVE_SCHEMA } from "@neko/llm/workflows/fence-schemas";
import type { PolicySavePayload } from "@neko/llm/workflows/fence-schemas";

const NEKO_FENCE_STRIP_RE = /```neko_[a-z_]+\s*[\s\S]*?(?:```|$)/gi;
// Tolerant rule-save fence: closing ``` is optional. The LLM sometimes
// finishes a turn without emitting it — the fence is the last thing in
// the message, so parsing should still succeed.
const POLICY_FENCE_OPEN_TOLERANT_RE = /```neko_policy_save\s*([\s\S]*?)(?:```|$)/i;

export function stripNekoFences(raw: string): string {
  return raw.replace(NEKO_FENCE_STRIP_RE, "").replace(/\n{3,}/g, "\n\n").trim();
}

export function extractRuleSaveEvent(raw: string): PolicySavePayload | null {
  const strict = extractPolicySaveFence(raw).payload;
  if (strict) return strict;
  // Fall back to tolerant parse for messages without a closing fence.
  const m = raw.match(POLICY_FENCE_OPEN_TOLERANT_RE);
  if (!m) return null;
  try {
    const json = JSON.parse(m[1].trim());
    const validated = POLICY_SAVE_SCHEMA.safeParse(json);
    return validated.success ? validated.data : null;
  } catch {
    return null;
  }
}

function modeLabel(mode: PolicySavePayload["mode"]): string {
  if (mode === "auto_approve") return "auto-approves";
  if (mode === "approval_required") return "needs approval";
  if (mode === "observe_only") return "observe only";
  if (mode === "draft_only") return "draft only";
  if (mode === "never") return "never fires";
  return mode;
}

export function RuleSavedCard({ payload }: { payload: PolicySavePayload }) {
  const limits =
    payload.limits && Object.keys(payload.limits).length > 0
      ? Object.entries(payload.limits)
          .map(([k, v]) => `${k.replace(/_/g, " ")} ${v}`)
          .join(", ")
      : null;

  return (
    <article className="rule-event">
      <span className="rule-event-tick" aria-hidden="true">✓</span>
      <div className="rule-event-body">
        <div className="rule-event-eyebrow">Rule saved</div>
        <div className="rule-event-name">{payload.name}</div>
        <div className="rule-event-meta">
          <span className="rule-event-mode">{modeLabel(payload.mode)}</span>
          {limits && (
            <>
              <span className="rule-event-sep">·</span>
              <span className="rule-event-limits">{limits}</span>
            </>
          )}
        </div>
      </div>
    </article>
  );
}
