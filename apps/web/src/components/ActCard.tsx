"use client";

import { useRouter } from "next/navigation";
import { Card } from "@/components/ui/Card";
import { Pill, type PillVariant } from "@/components/ui/Pill";
import { cn } from "@/lib/cn";
import { formatSavedShort } from "@/lib/hours-saved";

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
  /** Realized minutes saved — shown on fired receipts that carry an estimate. */
  minutesSaved?: number | null;
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

const STATE_PILL_VARIANT: Record<ActCardData["state"], PillVariant> = {
  live: "live",
  awaiting: "watch",
  rejected: "muted",
};

const TONE_DOT: Record<ActRowTone, string> = {
  good: "bg-[#7bd98a]",
  watch: "bg-[#f2c35f]",
  action: "bg-[#e06b6b]",
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
    <Card
      as="article"
      className={cn(
        "grid gap-3 act-card",
        data.state === "awaiting" && "border-watch/35",
      )}
      style={{ animation: `fadeUp 0.4s ease ${index * 0.04}s both` }}
    >
      <header className="flex items-center justify-between gap-3">
        <Pill variant={STATE_PILL_VARIANT[data.state]}>
          {STATE_LABEL[data.state]}
        </Pill>
        <span className="font-mono text-[11px] font-bold uppercase tracking-[0.08em] text-text3">
          {formatTime(data.runAt)}
        </span>
      </header>

      {data.trigger && (
        <div className="px-3.5 py-3 border border-watch/30 rounded-xl bg-watch/10 text-text font-semibold text-sm leading-snug">
          {data.trigger}
        </div>
      )}

      <ul className="list-none m-0 p-0 grid gap-2">
        {data.rows.map((row) => {
          const isFocused = focusedRowId === row.id;
          const isBusy = busyRowId === row.id;
          const isRejecting = rejectingRowId === row.id;
          const isPending = data.state === "awaiting";

          return (
            <li
              key={row.id}
              ref={(el) => rowRef?.(row.id, el)}
              className={cn(
                "flex items-start gap-3 px-3.5 py-3 rounded-xl border border-border bg-card cursor-pointer",
                "transition-[border-color,box-shadow] duration-150",
                "hover:border-text3",
                "focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2",
                isFocused && "border-accent shadow-[0_0_0_3px_var(--color-accent-soft)]",
              )}
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
              <span
                className={cn(
                  "w-2.5 h-2.5 mt-1.5 rounded-full flex-none",
                  TONE_DOT[row.tone],
                )}
              />
              <div className="flex-1 min-w-0 grid gap-1">
                <p className="m-0 text-[14.5px] font-semibold text-text leading-snug tracking-[-0.005em]">
                  {row.headline}
                </p>
                {row.target && (
                  <p className="m-0 font-mono text-xs text-text2 break-all">
                    {row.target}
                  </p>
                )}
                {row.detail && (
                  <p className="m-0 text-[13px] leading-[1.55] text-text2">
                    {row.detail}
                  </p>
                )}
                {row.rejectionReason && (
                  <p className="m-0 text-[13px] leading-[1.55] text-text2 italic">
                    {row.rejectionReason}
                  </p>
                )}
                {data.state === "live" && (row.approverPhrase || (row.minutesSaved ?? 0) > 0) && (
                  <p className="mt-1 text-[11.5px] text-text3 flex items-center gap-2 flex-wrap">
                    {row.approverPhrase && (
                      <span>
                        approved by{" "}
                        <span className="text-text2 font-medium">
                          {row.approverPhrase}
                        </span>
                      </span>
                    )}
                    {(row.minutesSaved ?? 0) > 0 && (
                      <span
                        className="font-mono text-success-ink bg-success-soft border border-success-mid/30 rounded-full px-1.5 py-px"
                        title="Estimated human time saved"
                      >
                        {formatSavedShort(row.minutesSaved as number)} saved
                      </span>
                    )}
                  </p>
                )}

                {isPending && isRejecting ? (
                  <div
                    className="flex flex-col gap-2 mt-2"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <textarea
                      className="border border-border rounded-[10px] px-3 py-2 text-[13px] text-text bg-card resize-y min-h-[50px] outline-none focus:border-accent"
                      value={rejectReason ?? ""}
                      placeholder="Why are you rejecting this? (optional)"
                      onChange={(e) => onRejectReasonChange?.(e.target.value)}
                      autoFocus
                      rows={2}
                    />
                    <div className="flex gap-2">
                      <RowButton tone="destructive" disabled={isBusy} onClick={onSubmitReject}>
                        Confirm reject
                      </RowButton>
                      <RowButton disabled={isBusy} onClick={onCancelReject}>
                        Cancel
                      </RowButton>
                    </div>
                  </div>
                ) : isPending ? (
                  <div className="flex gap-2 mt-2">
                    <RowButton
                      tone="primary"
                      disabled={isBusy}
                      onClick={(e) => {
                        e.stopPropagation();
                        onApproveRow?.(row.id);
                      }}
                    >
                      Approve
                    </RowButton>
                    <RowButton
                      disabled={isBusy}
                      onClick={(e) => {
                        e.stopPropagation();
                        onBeginRejectRow?.(row.id);
                      }}
                    >
                      Reject
                    </RowButton>
                    <a
                      className="ml-auto self-center px-1.5 py-1 text-xs font-semibold text-text2 no-underline opacity-70 transition-opacity duration-150 hover:opacity-100 hover:text-text hover:underline underline-offset-2"
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
    </Card>
  );
}

type RowButtonProps = {
  tone?: "primary" | "destructive";
  disabled?: boolean;
  onClick?: (e: React.MouseEvent<HTMLButtonElement>) => void;
  children: React.ReactNode;
};

function RowButton({ tone, disabled, onClick, children }: RowButtonProps) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "px-3 py-1.5 rounded-lg border text-[12.5px] font-semibold cursor-pointer",
        "disabled:opacity-55 disabled:cursor-not-allowed",
        !tone && "bg-card border-border text-text hover:not-disabled:border-text3",
        tone === "primary" &&
          "bg-success-ink border-success-ink text-white hover:not-disabled:bg-[#0b2912] hover:not-disabled:border-[#0b2912]",
        tone === "destructive" &&
          "bg-danger border-danger text-white hover:not-disabled:bg-[#c84545] hover:not-disabled:border-[#c84545]",
      )}
    >
      {children}
    </button>
  );
}
