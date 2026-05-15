"use client";

import { useRouter } from "next/navigation";

export type ActionReceiptCardData = {
  id: string;
  kind: string;
  target: string | null;
  summary: string | null;
  scope: string;
  riskLevel: string | null;
  status: string;
  executedAt: string | null;
  executionStatus: string;
  approverKind: "operator" | "policy" | "auto";
  approverLabel: string | null;
  workflow: { id: string; name: string } | null;
};

function formatRelative(iso: string | null): string {
  if (!iso) return "";
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

function approverPhrase(kind: ActionReceiptCardData["approverKind"], label: string | null): string {
  if (kind === "operator") return label ? `you · ${label}` : "you";
  if (kind === "policy") return label ? `rule "${label}"` : "a rule";
  return "auto";
}

export default function ActionReceiptCard({
  data,
  index,
}: {
  data: ActionReceiptCardData;
  index: number;
}) {
  const router = useRouter();
  const title = data.summary || `${data.kind}${data.target ? ` → ${data.target}` : ""}`;
  const failed = data.executionStatus !== "succeeded" && data.executionStatus !== "executed";
  const pillLabel = failed ? "FAILED" : "FIRED";
  const pillClass = failed
    ? "finding-card-pill finding-card-pill-mood-act"
    : "finding-card-pill finding-card-pill-mood-good";

  const onOpen = () => router.push(`/actions/${data.id}`);

  return (
    <article
      className="finding-card"
      style={{ animation: `fadeUp 0.4s ease ${index * 0.04}s both` }}
      onClick={onOpen}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen();
        }
      }}
    >
      <div className="finding-card-head">
        <h3 className="finding-card-title">{title}</h3>
        <span className={pillClass}>{pillLabel}</span>
      </div>

      {data.target && data.summary && (
        <div className="finding-card-target">
          <span className="finding-card-mono">{data.target}</span>
        </div>
      )}

      <div className="finding-card-byline">
        <span>
          approved by{" "}
          <span className="finding-card-workflow">
            {approverPhrase(data.approverKind, data.approverLabel)}
          </span>
        </span>
        {data.workflow && (
          <>
            <span className="finding-card-sep">·</span>
            <span>
              from{" "}
              <span className="finding-card-workflow">{data.workflow.name}</span>
            </span>
          </>
        )}
        <span className="finding-card-sep">·</span>
        <span className="finding-card-mono">{formatRelative(data.executedAt)}</span>
        <span className="finding-card-drill">see receipt →</span>
      </div>
    </article>
  );
}
