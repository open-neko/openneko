"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Skeleton } from "@/components/ui/skeleton";
import { presentWorkFailure } from "@/lib/work-failure";
import type { OfflineQueueLosses, QueuedQuestion } from "./work-offline-queue";
import type { HeldBlock } from "./work-streaming-text";
import type { WorkEvent } from "./work-types";

const ANSWER_CARD_PLACEHOLDER_MS = 60_000;

/**
 * A long run shows where its answer will appear, with the step it is on and
 * the data it has read, until the agent sends its card.
 */
export function AnswerInProgress({ since, step, sources }: { since?: string; step: string | null; sources: string[] }) {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const started = since ? Date.parse(since) : Date.now();
    const wait = Math.max(0, ANSWER_CARD_PLACEHOLDER_MS - (Date.now() - started));
    const id = window.setTimeout(() => setShown(true), wait);
    return () => window.clearTimeout(id);
  }, [since]);
  if (!shown) return null;
  return (
    <div className="work-surface-frame answer-in-progress" role="status" aria-live="polite">
      <div className="work-surface">
        <div className="answer-draft-note">
          <span className="answer-draft-pulse" aria-hidden="true" />
          Preparing the answer
        </div>
        {step ? <div className="answer-in-progress-step">{step}</div> : null}
        {sources.length > 0 ? (
          <div className="answer-in-progress-sources">
            <span>Read so far</span>
            <strong>{sources.slice(0, 6).join(" · ")}{sources.length > 6 ? ` · ${sources.length - 6} more` : ""}</strong>
          </div>
        ) : null}
        <div className="answer-in-progress-figures" aria-hidden="true">
          <Skeleton className="h-16" />
          <Skeleton className="h-16" />
          <Skeleton className="h-16" />
        </div>
        <Skeleton className="mt-3 h-28" />
      </div>
    </div>
  );
}

const HELD_BLOCK_LABEL: Record<HeldBlock, string> = {
  table: "Writing a table",
  code: "Writing a code block",
  card: "Preparing a card",
};

/** Stands in for a table, code block or card until the agent completes it. */
export function StreamingBlockPlaceholder({ block }: { block: HeldBlock }) {
  return (
    <div className="work-block-pending" role="status" aria-live="polite" data-block={block}>
      <div className="answer-draft-note">
        <span className="answer-draft-pulse" aria-hidden="true" />
        {HELD_BLOCK_LABEL[block]}
      </div>
      <Skeleton className="h-10" />
    </div>
  );
}

export function WorkFailureNotice({ message }: { message: string }) {
  const failure = presentWorkFailure(message);
  return (
    <div
      className="rounded-2xl border border-warn/40 bg-warn-soft px-3 py-2.5 text-ui-body-sm text-warn-ink"
      role="alert"
    >
      <p className="font-semibold">{failure.summary}</p>
      <Disclosure
        title="Technical details"
        className="mt-2 border-warn/40 bg-card/70"
      >
        <p className="break-words font-mono text-ui-caption text-text2">
          {failure.detail}
        </p>
      </Disclosure>
    </div>
  );
}

const OFFLINE_NOTICE =
  "ml-auto flex max-w-full flex-wrap items-center justify-end gap-2.5 rounded-xl border border-border bg-warn-soft px-3 py-1.5 text-ui-caption font-medium text-warn-ink";

function questions(count: number): string {
  return count === 1 ? "1 question" : `${count} questions`;
}

/** Tells once how many waiting questions this browser removed unsent. */
export function OfflineLossNotice({
  losses,
  onDismiss,
}: {
  losses: OfflineQueueLosses;
  onDismiss: () => void;
}) {
  const lines: string[] = [];
  if (losses.expired > 0) {
    lines.push(
      `${questions(losses.expired)} that waited for the network ${losses.expired === 1 ? "was" : "were"} not sent and removed after 7 days.`,
    );
  }
  if (losses.otherUser > 0) {
    lines.push(
      `${questions(losses.otherUser)} that another user typed in this browser ${losses.otherUser === 1 ? "was" : "were"} not sent and removed.`,
    );
  }
  return (
    <div role="status" data-testid="offline-lost-notice" className={OFFLINE_NOTICE}>
      <span>{lines.join(" ")}</span>
      <Button size="sm" variant="secondary" onClick={onDismiss}>
        Dismiss
      </Button>
    </div>
  );
}

/** Refused questions of other chats, with a link to each chat. */
export function OfflineFailedElsewhere({ items }: { items: readonly QueuedQuestion[] }) {
  const chats = new Map<string, string>();
  for (const item of items) {
    const href = item.threadId
      ? `${item.threadBase}/${item.threadId}`
      : item.threadBase === "/work"
        ? "/work"
        : null;
    if (href && !chats.has(href)) chats.set(href, item.content);
  }
  return (
    <div role="status" data-testid="offline-failed-elsewhere" className={OFFLINE_NOTICE}>
      <span>
        The server refused {questions(items.length)} in other chats.
      </span>
      {[...chats].map(([href, content]) => (
        <Button key={href} asChild size="sm" variant="secondary">
          <Link href={href} title={content}>
            Open “{content.length > 32 ? `${content.slice(0, 31)}…` : content}”
          </Link>
        </Button>
      ))}
    </div>
  );
}

export function NeedsInputNotice({
  request,
  onRespond,
}: {
  request: Extract<WorkEvent, { type: "needs_input" }>;
  onRespond: (text: string) => void;
}) {
  const questions: NonNullable<typeof request.questions> = request.questions?.length
    ? request.questions
    : [{
        id: "q1",
        header: undefined,
        question: request.question,
        options: request.options?.map((label) => ({ label })),
      }];
  return (
    <section className="work-surface-frame" aria-label="Clarification needed">
      <div className="work-surface">
        <div className="work-surface-eyebrow">
          <span className="work-surface-eyebrow-rule" aria-hidden="true" />
          Clarification
        </div>
        <div className="work-surface-title">A detail before I continue</div>
        {request.reason ? <div className="work-surface-sub">{request.reason}</div> : null}
        <div className="work-a2ui-layout is-column">
          {questions.map((question) => (
            <div key={question.id} className="work-a2ui-field">
              <span className="work-a2ui-label">
                {question.header ? `${question.header}: ` : ""}{question.question}
              </span>
              {question.options?.length ? (
                <div className="work-a2ui-chips">
                  {question.options.map((option) => (
                    <Button
                      key={option.label}
                      size="sm"
                      variant="secondary"
                      className="work-choice-btn"
                      onClick={() => onRespond(`${question.question}\n\n${option.label}`)}
                    >
                      <span>{option.label}</span>
                      <span aria-hidden="true">→</span>
                    </Button>
                  ))}
                </div>
              ) : (
                <Button
                  size="sm"
                  className="work-a2ui-button"
                  onClick={() => onRespond(`${question.question}\n\n`)}
                >
                  Answer in composer
                </Button>
              )}
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

export function CapabilityDeniedNotice({
  denial,
  canAdminister,
}: {
  denial: Extract<WorkEvent, { type: "capability_denied" }>;
  canAdminister: boolean;
}) {
  const endpoint = `${denial.host}${denial.port ? `:${denial.port}` : ""}`;
  return (
    <section className="work-capability-denied" role="status">
      <div className="work-capability-kicker">Network access blocked</div>
      <div className="work-capability-grid">
        <div>
          <h3>{endpoint} is outside this workspace</h3>
          <p>
            OpenNeko did not receive live data. The sandbox stayed
            default-deny, so this answer must not present fallback estimates as
            current facts.
          </p>
          <code>
            {denial.method ?? "REQUEST"} {denial.path ?? "/"}
          </code>
        </div>
        <div className="work-capability-recovery">
          <span>Safe recovery</span>
          {canAdminister ? (
            <p>
              Install a solution pack that connects this service. A pack
              declares the hosts it reaches, so access stays scoped.
            </p>
          ) : (
            <p>
              Contact an OpenNeko administrator to install a solution pack
              that connects this host.
            </p>
          )}
          <Link href={canAdminister ? "/admin/settings/packs" : "/integrations"}>
            {canAdminister ? "Review solution packs" : "View integrations"}
          </Link>
        </div>
      </div>
    </section>
  );
}
