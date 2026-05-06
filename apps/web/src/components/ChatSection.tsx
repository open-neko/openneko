"use client";

import { useEffect, useState } from "react";
import { Copy, Pencil, RotateCcw, Trash2 } from "lucide-react";
import Chart from "./Chart";
import type { ChartDataPoint } from "./Chart";

export interface ChatMsg {
  id?: string;
  metricId?: string;
  type: "user" | "ai";
  text: string;
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

  return (
    <div className="cbubble cbai">
      <div className="bub">
        {msg.text}
        {msg.chartType && msg.chartData && (
          <div className="cchart">
            <Chart type={msg.chartType} h={110} data={msg.chartData} centerLabel={msg.metric} valueLabel={msg.label} />
          </div>
        )}
      </div>
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
