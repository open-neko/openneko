"use client";

// Lean briefing card tuned for findings (workflow_outputs) and approvals.
// Distinct from the existing BriefingCard which is heavy/KPI-shaped.

import { useRouter } from "next/navigation";

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

export default function FindingCard({
  data,
  index,
  onUnpin,
}: {
  data: FindingCardData;
  index: number;
  onUnpin?: (pinId: string) => void;
}) {
  const router = useRouter();
  const isApproval = data.kind === "approval";
  const pillLabel = isApproval
    ? data.riskLevel ?? "pending"
    : data.mood ?? "watch";
  const pillClass = isApproval
    ? `finding-card-pill finding-card-pill-risk-${data.riskLevel ?? "unknown"}`
    : `finding-card-pill finding-card-pill-mood-${data.mood ?? "watch"}`;

  const onDrillIn = () => {
    if (isApproval) {
      router.push("/approvals");
    } else if (data.workflowRunId) {
      router.push(`/runs/${data.workflowRunId}`);
    }
  };

  return (
    <article
      className="finding-card"
      style={{ animation: `fadeUp 0.4s ease ${index * 0.04}s both` }}
      onClick={onDrillIn}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onDrillIn();
        }
      }}
    >
      <div className="finding-card-head">
        <h3 className="finding-card-title">{data.title}</h3>
        <span className={pillClass}>{pillLabel}</span>
      </div>

      {data.body && <p className="finding-card-body">{data.body}</p>}

      {isApproval && data.target && (
        <div className="finding-card-target">
          <span className="finding-card-mono">{data.target}</span>
        </div>
      )}

      <div className="finding-card-byline">
        <span>
          from{" "}
          <span className="finding-card-workflow">{data.workflow.name}</span>
        </span>
        <span className="finding-card-sep">·</span>
        <span className="finding-card-mono">{formatRelative(data.createdAt)}</span>
        {data.pinId && onUnpin && (
          <button
            type="button"
            className="finding-card-unpin"
            onClick={(e) => {
              e.stopPropagation();
              onUnpin(data.pinId as string);
            }}
            title="Unpin from briefing"
          >
            unpin
          </button>
        )}
        <span className="finding-card-drill">
          {isApproval ? "open approvals →" : "drill in →"}
        </span>
      </div>
    </article>
  );
}
