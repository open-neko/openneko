"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { ArrowUp, MessageCircle } from "lucide-react";
import Link from "next/link";
import {
  buildRecordAskSeed,
  recordAskThreadTitle,
  type RecordAskContext,
} from "@/lib/record-ask";
import type { PendingRecordAction } from "@/lib/records-pending";
import { RecordActionDiff } from "./RecordActionDiff";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";

export type RecordAskPendingAction = {
  recordId: string;
  recordLabel: string;
  action: PendingRecordAction;
};

export type RecordAskScenario = {
  request: string;
  response: string;
  autoAction?: string;
};

export function RecordAskBox({
  context,
  pendingActions = [],
  scenario,
}: {
  context: RecordAskContext;
  pendingActions?: RecordAskPendingAction[];
  scenario?: RecordAskScenario;
}) {
  const router = useRouter();
  const [request, setRequest] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const detail =
    context.surface === "detail" || context.surface === "recycle_detail";
  const subject = detail ? "this record" : "these records";

  async function openAsk(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const message = request.trim();
    if (!message || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch("/api/work/threads", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          title: recordAskThreadTitle(context),
          recordContext: context,
        }),
      });
      if (!response.ok) throw new Error("Could not open Ask");
      const payload = (await response.json()) as { thread?: { id?: string } };
      if (!payload.thread?.id) throw new Error("Ask did not return a thread");
      const seed = buildRecordAskSeed(context, message);
      router.push(
        `/work/${payload.thread.id}?seed=${encodeURIComponent(seed)}`,
      );
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not open Ask");
      setSubmitting(false);
    }
  }

  const listSurface =
    context.surface === "list" || context.surface === "recycle_list";
  return (
    <aside
      className={`records-ask${listSurface ? " is-list-panel" : ""}`}
      aria-label={`Ask OpenNeko about ${subject}`}
    >
      <div className="records-ask-label">
        <MessageCircle aria-hidden="true" />
        <span>
          <strong>{listSurface ? "Ask" : `Ask about ${subject}`}</strong>
          <small>
            {listSurface
              ? `Scope: ${context.objectLabel}`
              : "Reads use your access. Changes still require policy approval."}
          </small>
        </span>
      </div>
      {listSurface && (
        <div className="records-ask-thread">
          {scenario && (
            <>
              <p className="records-ask-message is-user">{scenario.request}</p>
              <p className="records-ask-message is-agent">
                {scenario.response}
              </p>
            </>
          )}
          {pendingActions
            .slice(0, 3)
            .map(({ recordId, recordLabel, action }) => (
              <article className="records-ask-action" key={action.id}>
                <header>
                  <span>Approval needed</span>
                  <code>{action.kind}</code>
                </header>
                <div>
                  <strong>{recordLabel}</strong>
                  <small>
                    {context.objectLabel} · {recordId}
                  </small>
                  <RecordActionDiff
                    compact
                    kind={action.kind}
                    payload={{
                      app: context.appId,
                      object: context.objectApiName,
                      id: recordId,
                      fields: action.fields,
                      expected: action.expected,
                    }}
                  />
                </div>
                <Link href={`/actions/${action.id}`}>Review change</Link>
              </article>
            ))}
          {scenario?.autoAction && (
            <p className="records-ask-auto">
              <span aria-hidden="true">✓</span>
              {scenario.autoAction}
            </p>
          )}
          {!scenario && pendingActions.length === 0 && (
            <p className="records-ask-empty">
              This thread is scoped to the current view. Reads use your access;
              proposed changes keep their normal approval policy.
            </p>
          )}
        </div>
      )}
      <form onSubmit={openAsk}>
        <label className="sr-only" htmlFor={`records-ask-${context.surface}`}>
          Ask OpenNeko about {subject}
        </label>
        <Input
          id={`records-ask-${context.surface}`}
          value={request}
          onChange={(event) => {
            setRequest(event.target.value);
            if (error) setError(null);
          }}
          placeholder={
            detail
              ? "Summarize, update, or investigate this record…"
              : "Find, compare, or change a record…"
          }
          maxLength={2_000}
          disabled={submitting}
        />
        <Button
          variant="ghost"
          size="icon"
          type="submit"
          disabled={!request.trim() || submitting}
          aria-label={submitting ? "Opening Ask" : "Open Ask"}
        >
          {submitting ? (
            <Spinner className="records-spin" aria-hidden="true" />
          ) : (
            <ArrowUp aria-hidden="true" />
          )}
        </Button>
      </form>
      {error && (
        <span className="records-ask-error" role="alert">
          {error}
        </span>
      )}
    </aside>
  );
}
