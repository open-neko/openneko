"use client";

// Lean briefing card tuned for findings (workflow_outputs) and approvals.
// Distinct from the existing BriefingCard which is heavy/KPI-shaped.

import { useState } from "react";
import { useRouter } from "next/navigation";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Card } from "@/components/ui/Card";
import { Pill, type PillVariant } from "@/components/ui/Pill";
import { cn } from "@/lib/cn";

const MUTE_DURATIONS = ["1h", "24h", "7d"] as const;

export type FindingCardData = {
  id: string;
  kind: "approval" | "finding";
  workflowRunId: string | null;
  workflow: { id: string; name: string };
  title: string;
  body?: string | null;
  scope?: string | null;
  target?: string | null;
  mood?: string | null;
  outputKind?: string | null;
  riskLevel?: string | null;
  /** OL8: occurrences within the dedupe window ("2× today" badge when > 1). */
  seenCount?: number;
  lastSeenAt?: string;
  createdAt: string;
  pinId?: string;
  pinnedAt?: string;
};

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime();
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return new Date(iso).toLocaleDateString("en-IN", {
    month: "short",
    day: "numeric",
  });
}

function moodVariant(mood?: string | null): PillVariant {
  switch (mood) {
    case "good": return "success";
    case "watch": return "watch";
    case "act": return "danger";
    default: return "muted";
  }
}

function riskVariant(risk?: string | null): PillVariant {
  switch (risk) {
    case "low": return "muted";
    case "medium": return "watch";
    case "high":
    case "critical":
      return "danger";
    default: return "muted";
  }
}

export default function FindingCard({
  data,
  index,
  onUnpin,
  onMuted,
}: {
  data: FindingCardData;
  index: number;
  onUnpin?: (pinId: string) => void;
  /** OL7: present on Briefing cards — right-click offers "mute scope". */
  onMuted?: () => void;
}) {
  const router = useRouter();
  const [muteMenu, setMuteMenu] = useState<{ x: number; y: number } | null>(
    null,
  );
  const isApproval = data.kind === "approval";

  const muteScope = async (duration: (typeof MUTE_DURATIONS)[number]) => {
    setMuteMenu(null);
    if (!data.scope) return;
    try {
      await fetch("/api/briefing/mute", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scope: data.scope, duration }),
      });
      onMuted?.();
    } catch {
      // best-effort
    }
  };
  const pillLabel = isApproval
    ? data.riskLevel ?? "pending"
    : data.mood ?? "watch";
  const pillVariant = isApproval
    ? riskVariant(data.riskLevel)
    : moodVariant(data.mood);

  const onDrillIn = () => {
    if (isApproval) {
      router.push("/actions");
    } else if (data.workflowRunId) {
      router.push(`/runs/${data.workflowRunId}`);
    }
  };

  return (
    <Card
      as="article"
      className={cn(
        "group px-5 py-4 cursor-pointer transition-[border-color,transform] duration-200",
        "hover:border-text3 hover:-translate-y-px",
        "focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2",
      )}
      style={{ animation: `fadeUp 0.4s ease ${index * 0.04}s both` }}
      onClick={onDrillIn}
      onContextMenu={(e) => {
        if (!onMuted || !data.scope) return;
        e.preventDefault();
        setMuteMenu({ x: e.clientX, y: e.clientY });
      }}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onDrillIn();
        }
      }}
    >
      <div className="flex items-start justify-between gap-3 mb-1.5">
        <h3 className="font-display text-[17px] font-bold tracking-[-0.01em] text-text leading-snug m-0">
          {data.title}
        </h3>
        <Pill variant={pillVariant} className="flex-shrink-0">
          {pillLabel}
        </Pill>
      </div>

      {data.body && (
        <div className="work-markdown mb-2.5 text-sm leading-[1.55] text-text">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{data.body}</ReactMarkdown>
        </div>
      )}

      {isApproval && data.target && (
        <div className="mb-2">
          <span className="font-mono text-xs text-text2">{data.target}</span>
        </div>
      )}

      <div className="flex items-center gap-1.5 text-xs text-text3 flex-wrap">
        <span>
          from{" "}
          <span className="text-text2 font-medium">{data.workflow.name}</span>
        </span>
        <span className="opacity-50">·</span>
        <span className="font-mono text-xs text-text2">{formatRelative(data.createdAt)}</span>
        {(data.seenCount ?? 1) > 1 && (
          <>
            <span className="opacity-50">·</span>
            <span
              className="font-mono text-xs text-text2"
              title={
                data.lastSeenAt
                  ? `last seen ${formatRelative(data.lastSeenAt)}`
                  : undefined
              }
            >
              {data.seenCount}× today
            </span>
          </>
        )}
        {data.pinId && onUnpin && (
          <button
            type="button"
            className="bg-transparent border-0 text-text3 font-[inherit] text-[11.5px] p-0 cursor-pointer hover:text-danger hover:underline underline-offset-2"
            onClick={(e) => {
              e.stopPropagation();
              onUnpin(data.pinId as string);
            }}
            title="Unpin from briefing"
          >
            unpin
          </button>
        )}
        <span className="ml-auto text-xs text-accent group-hover:underline underline-offset-2">
          {isApproval ? "open approvals →" : "drill in →"}
        </span>
      </div>

      {muteMenu && (
        <div
          className="fixed inset-0 z-50"
          onClick={(e) => {
            e.stopPropagation();
            setMuteMenu(null);
          }}
          onContextMenu={(e) => {
            e.preventDefault();
            setMuteMenu(null);
          }}
        >
          <div
            className="absolute bg-bg border-[1.5px] border-border rounded-xl py-1.5 shadow-lg min-w-[180px]"
            style={{ left: muteMenu.x, top: muteMenu.y }}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="px-3 py-1 text-[11px] text-text3">
              Mute <span className="font-mono">{data.scope}</span>
            </div>
            {MUTE_DURATIONS.map((d) => (
              <button
                key={d}
                type="button"
                className="block w-full text-left bg-transparent border-0 px-3 py-1.5 text-[13px] text-text cursor-pointer hover:bg-bg2 font-[inherit]"
                onClick={(e) => {
                  e.stopPropagation();
                  void muteScope(d);
                }}
              >
                for {d}
              </button>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}
