import { enqueue, QUEUE, type LibraryDistillPayload } from "@neko/db/jobs";
import { markLibraryDocumentStatus, runLibraryDistill } from "@neko/llm";
import { createWorkerHarnessObserver } from "../telemetry.js";

const MAX_DISTILL_ATTEMPTS = 5;

type DistillDependencies = {
  enqueue: typeof enqueue;
  run: typeof runLibraryDistill;
  markStatus: typeof markLibraryDocumentStatus;
};

const defaultDependencies: DistillDependencies = {
  enqueue,
  run: runLibraryDistill,
  markStatus: markLibraryDocumentStatus,
};

/**
 * library_distill job handler: run the librarian over one uploaded
 * document, writing OKF concepts onto the uploader's personal layer.
 * Status transitions (distilling → cataloged | skipped | failed) live on
 * the library_document row, so the web UI polls the DB, not pg-boss.
 * A failed model/provider call schedules a new durable job with exponential
 * backoff. The distillation checkpoint advances only after a valid chunk, so
 * malformed output retries that chunk without silently cataloging it.
 */
export async function runLibraryDistillJob(
  payload: LibraryDistillPayload,
  overrides: Partial<DistillDependencies> = {},
): Promise<void> {
  const deps = { ...defaultDependencies, ...overrides };
  try {
    const result = await deps.run({
      orgId: payload.orgId,
      documentId: payload.documentId,
      force: payload.force === true,
      observer: createWorkerHarnessObserver(`${payload.runId}:${payload.sequence}`).observer,
    });
    console.log(
      `[library-distill] document ${payload.documentId} → ${result.status}` +
        (result.reason ? ` (${result.reason})` : "") +
        (result.concepts.length > 0
          ? `, ${result.concepts.length} concept(s)`
          : ""),
    );
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const attempt = payload.attempt ?? 0;
    if (attempt + 1 < MAX_DISTILL_ATTEMPTS) {
      const nextAttempt = attempt + 1;
      const sequence = payload.sequence + 1;
      await deps.enqueue(
        QUEUE.LIBRARY_DISTILL,
        { ...payload, sequence, attempt: nextAttempt },
        {
          startAfter: Math.min(300, 15 * 2 ** attempt),
          retryLimit: MAX_DISTILL_ATTEMPTS,
          retryDelay: 30,
          retryBackoff: true,
          singletonKey: `library-distill:${payload.documentId}:${payload.runId}:${sequence}`,
        },
      );
      console.warn(
        `[library-distill] document ${payload.documentId} retry ${nextAttempt}/${MAX_DISTILL_ATTEMPTS - 1}: ${message}`,
      );
      return;
    }
    await deps.markStatus({
      orgId: payload.orgId,
      id: payload.documentId,
      status: "failed",
      error: message.slice(0, 2_000),
    });
    console.error(`[library-distill] document ${payload.documentId} failed: ${message}`);
  }
}
