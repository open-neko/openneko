"use client";

import { extractPolicySaveFence } from "@neko/llm/workflows/fences";
import type { PolicySavePayload } from "@neko/llm/workflows/fence-schemas";

const NEKO_FENCE_STRIP_RE = /```neko_[a-z_]+\s*[\s\S]*?(?:```|$)/gi;

export function stripNekoFences(raw: string): string {
  return raw.replace(NEKO_FENCE_STRIP_RE, "").replace(/\n{3,}/g, "\n\n").trim();
}

export function extractRuleSaveEvent(raw: string): PolicySavePayload | null {
  return extractPolicySaveFence(raw).payload;
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
