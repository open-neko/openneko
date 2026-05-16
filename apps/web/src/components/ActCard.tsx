"use client";

import { useRouter } from "next/navigation";

export type ActRowTone = "good" | "watch" | "action";

export type ActRowData = {
  id: string;
  tone: ActRowTone;
  headline: string;
  detail?: string | null;
  target?: string | null;
  rejectionReason?: string | null;
  approverPhrase?: string | null;
  status: string;
};

export type ActCardData = {
  runId: string | null;
  runAt: string;
  trigger?: string | null;
  state: "live" | "awaiting" | "rejected";
  workflowName?: string | null;
  rows: ActRowData[];
};

function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-IN", {
    hour: "numeric",
    minute: "2-digit",
    hour12: true,
  }).toUpperCase();
}

const STATE_LABEL: Record<ActCardData["state"], string> = {
  live: "Auto-response live",
  awaiting: "Awaiting you",
  rejected: "Rejected",
};

const STATE_PILL_CLASS: Record<ActCardData["state"], string> = {
  live: "act-pill act-pill-live",
  awaiting: "act-pill act-pill-awaiting",
  rejected: "act-pill act-pill-rejected",
};

export default function ActCard({
  data,
  index,
  focusedRowId,
  busyRowId,
  rejectingRowId,
  rejectReason,
  onRejectReasonChange,
  onCancelReject,
  onSubmitReject,
  onFocusRow,
  onApproveRow,
  onBeginRejectRow,
  rowRef,
}: {
  data: ActCardData;
  index: number;
  focusedRowId?: string | null;
  busyRowId?: string | null;
  rejectingRowId?: string | null;
  rejectReason?: string;
  onRejectReasonChange?: (v: string) => void;
  onCancelReject?: () => void;
  onSubmitReject?: () => void;
  onFocusRow?: (id: string) => void;
  onApproveRow?: (id: string) => void;
  onBeginRejectRow?: (id: string) => void;
  rowRef?: (id: string, el: HTMLLIElement | null) => void;
}) {
  const router = useRouter();
  return (
    <article
      className={`act-card act-card-${data.state}`}
      style={{ animation: `fadeUp 0.4s ease ${index * 0.04}s both` }}
    >
      <header className="act-card-topbar">
        <span className={STATE_PILL_CLASS[data.state]}>
          {STATE_LABEL[data.state]}
        </span>
        <span className="act-time">{formatTime(data.runAt)}</span>
      </header>

      {data.trigger && <div className="act-trigger">{data.trigger}</div>}

      <ul className="act-rows">
        {data.rows.map((row) => {
          const isFocused = focusedRowId === row.id;
          const isBusy = busyRowId === row.id;
          const isRejecting = rejectingRowId === row.id;
          const isPending = data.state === "awaiting";

          return (
            <li
              key={row.id}
              ref={(el) => rowRef?.(row.id, el)}
              className={`act-row${isFocused ? " is-focused" : ""}`}
              onClick={() => {
                if (isPending) onFocusRow?.(row.id);
                else router.push(`/actions/${row.id}`);
              }}
              role="button"
              tabIndex={0}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  if (isPending) onFocusRow?.(row.id);
                  else router.push(`/actions/${row.id}`);
                }
              }}
            >
              <span className={`act-row-dot act-row-${row.tone}`} />
              <div className="act-row-body">
                <p className="act-row-headline">{row.headline}</p>
                {row.target && (
                  <p className="act-row-mono">{row.target}</p>
                )}
                {row.detail && <p className="act-row-detail">{row.detail}</p>}
                {row.rejectionReason && (
                  <p className="act-row-detail act-row-reason">
                    {row.rejectionReason}
                  </p>
                )}
                {data.state === "live" && row.approverPhrase && (
                  <p className="act-row-byline">
                    approved by{" "}
                    <span className="act-row-byline-strong">
                      {row.approverPhrase}
                    </span>
                  </p>
                )}

                {isPending && isRejecting ? (
                  <div
                    className="act-row-reject"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <textarea
                      className="act-row-reject-input"
                      value={rejectReason ?? ""}
                      placeholder="Why are you rejecting this? (optional)"
                      onChange={(e) => onRejectReasonChange?.(e.target.value)}
                      autoFocus
                      rows={2}
                    />
                    <div className="act-row-reject-actions">
                      <button
                        type="button"
                        className="act-row-btn act-row-btn-destructive"
                        disabled={isBusy}
                        onClick={onSubmitReject}
                      >
                        Confirm reject
                      </button>
                      <button
                        type="button"
                        className="act-row-btn"
                        disabled={isBusy}
                        onClick={onCancelReject}
                      >
                        Cancel
                      </button>
                    </div>
                  </div>
                ) : isPending ? (
                  <div className="act-row-actions">
                    <button
                      type="button"
                      className="act-row-btn act-row-btn-primary"
                      disabled={isBusy}
                      onClick={(e) => {
                        e.stopPropagation();
                        onApproveRow?.(row.id);
                      }}
                    >
                      [a] Approve
                    </button>
                    <button
                      type="button"
                      className="act-row-btn"
                      disabled={isBusy}
                      onClick={(e) => {
                        e.stopPropagation();
                        onBeginRejectRow?.(row.id);
                      }}
                    >
                      [r] Reject
                    </button>
                    <a
                      className="act-row-why"
                      href={`/actions/${row.id}`}
                      onClick={(e) => e.stopPropagation()}
                      title="Open the full lineage: trigger, workflow, payload"
                    >
                      why →
                    </a>
                  </div>
                ) : null}
              </div>
            </li>
          );
        })}
      </ul>
    </article>
  );
}
