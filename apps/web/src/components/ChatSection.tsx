"use client";

import { useEffect, useState } from "react";
import { Copy, Pencil, RotateCcw, Trash2 } from "lucide-react";
import BriefingCard, { type BriefingCardData } from "./BriefingCard";
import type { ChartDataPoint } from "./Chart";

/**
 * AI chat answers render as a BriefingCard so they get the same treatment
 * as dashboard cards: mood dot, title, headline metric/delta (kpi) or
 * chart (non-kpi), expandable insight + detail text. Anything we render
 * as just text is a code-path bug — see commit history for why.
 *
 * `text` on AI messages is reserved for transient states the briefing
 * card shape can't represent (network errors, demo-mode mock answers).
 * Once `card` is populated, `text` is ignored.
 */
export interface ChatMsg {
  id?: string;
  metricId?: string;
  type: "user" | "ai";
  text: string;
  /** Populated once a metric_refresh succeeds — drives the BriefingCard render. */
  card?: BriefingCardData;
  /** Skeleton chart for the in-flight state, used until `card` lands. */
  metric?: string;
  label?: string;
  chartType?: string;
  chartData?: ChartDataPoint[];
}

interface ChatBubbleProps {
  msg: ChatMsg;
  isEditing?: boolean;
  busy?: boolean;
  onStartEdit?: (id: string) => void;
  onCancelEdit?: () => void;
  onSubmitEdit?: (id: string, text: string) => void;
  onRerun?: (id: string) => void;
  onDelete?: (id: string) => void;
}

export function ChatBubble({
  msg,
  isEditing,
  busy,
  onStartEdit,
  onCancelEdit,
  onSubmitEdit,
  onRerun,
  onDelete,
}: ChatBubbleProps) {
  const [draft, setDraft] = useState(msg.text);

  // Reset draft to the message text every time editing begins so an earlier
  // cancelled edit doesn't leak its draft into the next edit session.
  useEffect(() => {
    if (isEditing) setDraft(msg.text);
  }, [isEditing, msg.text]);

  if (msg.type === "user") {
    if (isEditing && msg.id) {
      const submit = () => {
        const trimmed = draft.trim();
        if (!trimmed) return;
        onSubmitEdit?.(msg.id!, trimmed);
      };
      return (
        <div className="cbubble cbuser">
          <div className="bub bubedit">
            <textarea
              className="bubedit-input"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  submit();
                } else if (e.key === "Escape") {
                  onCancelEdit?.();
                }
              }}
              autoFocus
              rows={2}
            />
            <div className="bubedit-actions">
              <button className="bubedit-btn" onClick={() => onCancelEdit?.()}>
                Cancel
              </button>
              <button className="bubedit-btn primary" onClick={submit} disabled={!draft.trim()}>
                Save & rerun
              </button>
            </div>
          </div>
        </div>
      );
    }

    const canAct = !!msg.id && !busy;
    // Copy + Delete are local UI ops — allowed even mid-flight
    const canDelete = !!msg.id;
    return (
      <div className="cbubble cbuser">
        <div className="bub">{msg.text}</div>
        <div className="cbactions">
          <button
            className="cbaction"
            title="Copy"
            onClick={() => { void navigator.clipboard?.writeText(msg.text); }}
          >
            <Copy size={14} strokeWidth={2} />
          </button>
          {canAct && (
            <button
              className="cbaction"
              title="Rerun"
              onClick={() => onRerun?.(msg.id!)}
            >
              <RotateCcw size={14} strokeWidth={2} />
            </button>
          )}
          {canAct && (
            <button
              className="cbaction"
              title="Edit"
              onClick={() => onStartEdit?.(msg.id!)}
            >
              <Pencil size={14} strokeWidth={2} />
            </button>
          )}
          {canDelete && (
            <button
              className="cbaction cbaction-danger"
              title="Delete"
              onClick={() => onDelete?.(msg.id!)}
            >
              <Trash2 size={14} strokeWidth={2} />
            </button>
          )}
        </div>
      </div>
    );
  }

  // Once the worker job succeeds, the AI answer is a fully-formed card —
  // render it with the same BriefingCard component the dashboard uses so
  // it gets mood, kpi-or-chart, and the expandable detail text.
  if (msg.card) {
    return (
      <div className="cbubble cbai">
        <BriefingCard ins={msg.card} index={0} onDismiss={() => { /* no-op in chat */ }} />
      </div>
    );
  }

  // Pre-result fallback: skeleton or a transient text-only message
  // (network error, demo-mode mock).
  return (
    <div className="cbubble cbai">
      <div className="bub">{msg.text}</div>
    </div>
  );
}

export function TypingIndicator() {
  return (
    <div className="cbubble cbai">
      <div className="bub">
        <div className="typing">
          <div className="tdot" />
          <div className="tdot" />
          <div className="tdot" />
        </div>
      </div>
    </div>
  );
}
