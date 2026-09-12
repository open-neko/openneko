import { enqueue, QUEUE, type LibraryExtractPayload, type LibraryDistillPayload } from "@neko/db/jobs";
import {
  clearLibraryTransientState,
  completeLibraryExtraction,
  extractDirectText,
  fetchLibrarianExtraction,
  getLibraryDocument,
  getLibraryExtractCheckpoint,
  librarianServiceUrl,
  libraryExtractionKind,
  libraryExtractorFingerprint,
  markLibraryDocumentStatus,
  pollLibrarianExtraction,
  readLibraryDerivedMarkdown,
  removeLibraryDerivedMarkdown,
  resolveLibrarySourcePath,
  RetryableLibraryExtractionError,
  saveLibraryExtractCheckpoint,
  submitLibrarianExtraction,
  TerminalLibraryExtractionError,
  writeLibraryDerivedMarkdown,
} from "@neko/llm";

const MAX_TRANSIENT_ATTEMPTS = 8;
const POLL_DELAY_SECONDS = 3;
const MAX_TASK_AGE_MS = 2 * 60 * 60 * 1_000;

type ExtractCheckpoint = {
  v: 1;
  sourceContentHash: string;
  extractorFingerprint: string;
  taskId: string;
  pollCount: number;
  submittedAt: string;
};

type ExtractDependencies = {
  enqueue: typeof enqueue;
  getDocument: typeof getLibraryDocument;
  getCheckpoint: typeof getLibraryExtractCheckpoint;
  saveCheckpoint: typeof saveLibraryExtractCheckpoint;
  completeExtraction: typeof completeLibraryExtraction;
  clearTransientState: typeof clearLibraryTransientState;
  markStatus: typeof markLibraryDocumentStatus;
  resolveSourcePath: typeof resolveLibrarySourcePath;
  serviceUrl: typeof librarianServiceUrl;
  submit: typeof submitLibrarianExtraction;
  poll: typeof pollLibrarianExtraction;
  fetchResult: typeof fetchLibrarianExtraction;
  extractText: typeof extractDirectText;
  writeDerived: typeof writeLibraryDerivedMarkdown;
  readDerived: typeof readLibraryDerivedMarkdown;
  removeDerived: typeof removeLibraryDerivedMarkdown;
};

const defaultDependencies: ExtractDependencies = {
  enqueue,
  getDocument: getLibraryDocument,
  getCheckpoint: getLibraryExtractCheckpoint,
  saveCheckpoint: saveLibraryExtractCheckpoint,
  completeExtraction: completeLibraryExtraction,
  clearTransientState: clearLibraryTransientState,
  markStatus: markLibraryDocumentStatus,
  resolveSourcePath: resolveLibrarySourcePath,
  serviceUrl: librarianServiceUrl,
  submit: submitLibrarianExtraction,
  poll: pollLibrarianExtraction,
  fetchResult: fetchLibrarianExtraction,
  extractText: extractDirectText,
  writeDerived: writeLibraryDerivedMarkdown,
  readDerived: readLibraryDerivedMarkdown,
  removeDerived: removeLibraryDerivedMarkdown,
};

/**
 * Durable extraction state machine. Binary documents are submitted once and
 * polled by short pg-boss jobs; direct Markdown/text inputs skip Docling. The
 * database checkpoint is the source of truth, so a worker restart resumes and
 * a lost in-memory librarian task is safely resubmitted.
 */
export async function runLibraryExtractJob(
  payload: LibraryExtractPayload,
  overrides: Partial<ExtractDependencies> = {},
): Promise<void> {
  const deps = { ...defaultDependencies, ...overrides };
  try {
    const document = await deps.getDocument(payload.orgId, payload.documentId);
    if (!document) throw new TerminalLibraryExtractionError("library document not found");
    if (document.status === "cataloged" || (!payload.force && document.status === "skipped")) {
      return;
    }

    const kind = libraryExtractionKind(document.filename);
    if (!kind) {
      throw new TerminalLibraryExtractionError(
        `Unsupported library file type: ${document.filename}`,
      );
    }
    const fingerprint = libraryExtractorFingerprint(kind);
    await deps.markStatus({
      orgId: payload.orgId,
      id: document.id,
      status: "extracting",
    });

    if (
      document.extractedRelativePath &&
      document.extractedContentHash &&
      document.extractorFingerprint === fingerprint
    ) {
      try {
        await deps.readDerived({
          orgId: payload.orgId,
          relativePath: document.extractedRelativePath,
          expectedHash: document.extractedContentHash,
        });
        await deps.completeExtraction({
          orgId: payload.orgId,
          id: document.id,
          relativePath: document.extractedRelativePath,
          contentHash: document.extractedContentHash,
          extractorFingerprint: fingerprint,
        });
        await enqueueDistill(deps, payload);
        return;
      } catch {
        await deps.removeDerived(payload.orgId, document.extractedRelativePath);
        await deps.clearTransientState(payload.orgId, document.id);
      }
    } else if (document.extractedRelativePath) {
      await deps.removeDerived(payload.orgId, document.extractedRelativePath);
      await deps.clearTransientState(payload.orgId, document.id);
    }

    const absolutePath = deps.resolveSourcePath(payload.orgId, document.relativePath);
    if (kind === "text") {
      const outcome = await deps.extractText(absolutePath);
      if (!outcome.ok) throw new TerminalLibraryExtractionError(outcome.reason);
      await finishExtraction(deps, payload, fingerprint, outcome.text);
      return;
    }

    const baseUrl = deps.serviceUrl();
    if (!baseUrl) {
      throw new RetryableLibraryExtractionError("NEKO_LIBRARIAN_URL is not configured");
    }
    let checkpoint = parseExtractCheckpoint(
      await deps.getCheckpoint(payload.orgId, document.id),
      document.contentHash,
      fingerprint,
    );
    if (!checkpoint) {
      checkpoint = await submitNewTask(
        deps,
        payload,
        document.contentHash,
        fingerprint,
        absolutePath,
        document.filename,
        baseUrl,
      );
      await scheduleExtract(deps, payload, 0, POLL_DELAY_SECONDS);
      return;
    }
    if (Date.now() - Date.parse(checkpoint.submittedAt) > MAX_TASK_AGE_MS) {
      const nextAttempt = (payload.attempt ?? 0) + 1;
      if (nextAttempt >= MAX_TRANSIENT_ATTEMPTS) {
        throw new TerminalLibraryExtractionError(
          "librarian conversion did not finish within two hours",
        );
      }
      await submitNewTask(
        deps,
        payload,
        document.contentHash,
        fingerprint,
        absolutePath,
        document.filename,
        baseUrl,
      );
      await scheduleExtract(deps, payload, nextAttempt, POLL_DELAY_SECONDS);
      return;
    }

    let status;
    try {
      status = await deps.poll(baseUrl, checkpoint.taskId);
    } catch (error) {
      if (error instanceof RetryableLibraryExtractionError && error.taskMissing) {
        await submitNewTask(
          deps,
          payload,
          document.contentHash,
          fingerprint,
          absolutePath,
          document.filename,
          baseUrl,
        );
        await scheduleExtract(deps, payload, 0, POLL_DELAY_SECONDS);
        return;
      }
      throw error;
    }
    if (status.state === "failure") {
      throw new TerminalLibraryExtractionError(
        status.error ? `librarian conversion failed: ${status.error}` : "librarian conversion failed",
      );
    }
    if (status.state !== "success") {
      const nextCheckpoint: ExtractCheckpoint = {
        ...checkpoint,
        pollCount: checkpoint.pollCount + 1,
      };
      await deps.saveCheckpoint(payload.orgId, document.id, nextCheckpoint);
      await scheduleExtract(
        deps,
        payload,
        0,
        Math.min(15, POLL_DELAY_SECONDS + nextCheckpoint.pollCount),
      );
      return;
    }

    try {
      const outcome = await deps.fetchResult(baseUrl, checkpoint.taskId);
      if (!outcome.ok) throw new TerminalLibraryExtractionError(outcome.reason);
      await finishExtraction(deps, payload, fingerprint, outcome.text);
    } catch (error) {
      if (error instanceof RetryableLibraryExtractionError && error.taskMissing) {
        await submitNewTask(
          deps,
          payload,
          document.contentHash,
          fingerprint,
          absolutePath,
          document.filename,
          baseUrl,
        );
        await scheduleExtract(deps, payload, 0, POLL_DELAY_SECONDS);
        return;
      }
      throw error;
    }
  } catch (error) {
    await handleExtractionError(deps, payload, error);
  }
}

async function finishExtraction(
  deps: ExtractDependencies,
  payload: LibraryExtractPayload,
  fingerprint: string,
  text: string,
): Promise<void> {
  const artifact = await deps.writeDerived({
    orgId: payload.orgId,
    documentId: payload.documentId,
    extractorFingerprint: fingerprint,
    text,
  });
  await deps.completeExtraction({
    orgId: payload.orgId,
    id: payload.documentId,
    relativePath: artifact.relativePath,
    contentHash: artifact.contentHash,
    extractorFingerprint: fingerprint,
  });
  await enqueueDistill(deps, payload);
}

async function submitNewTask(
  deps: ExtractDependencies,
  payload: LibraryExtractPayload,
  sourceContentHash: string,
  extractorFingerprint: string,
  absolutePath: string,
  filename: string,
  baseUrl: string,
): Promise<ExtractCheckpoint> {
  const taskId = await deps.submit({ absolutePath, filename, baseUrl });
  const checkpoint: ExtractCheckpoint = {
    v: 1,
    sourceContentHash,
    extractorFingerprint,
    taskId,
    pollCount: 0,
    submittedAt: new Date().toISOString(),
  };
  await deps.saveCheckpoint(payload.orgId, payload.documentId, checkpoint);
  return checkpoint;
}

function parseExtractCheckpoint(
  value: Record<string, unknown> | null,
  sourceContentHash: string,
  extractorFingerprint: string,
): ExtractCheckpoint | null {
  if (
    !value ||
    value.v !== 1 ||
    value.sourceContentHash !== sourceContentHash ||
    value.extractorFingerprint !== extractorFingerprint ||
    typeof value.taskId !== "string" ||
    typeof value.pollCount !== "number" ||
    typeof value.submittedAt !== "string" ||
    !Number.isInteger(value.pollCount) ||
    value.pollCount < 0 ||
    !Number.isFinite(Date.parse(value.submittedAt))
  ) {
    return null;
  }
  return value as ExtractCheckpoint;
}

async function enqueueDistill(
  deps: ExtractDependencies,
  payload: LibraryExtractPayload,
): Promise<void> {
  const next: LibraryDistillPayload = {
    orgId: payload.orgId,
    documentId: payload.documentId,
    runId: payload.runId,
    sequence: 0,
    attempt: 0,
    force: payload.force,
  };
  await deps.enqueue(QUEUE.LIBRARY_DISTILL, next, {
    retryLimit: MAX_TRANSIENT_ATTEMPTS,
    retryDelay: 15,
    retryBackoff: true,
    singletonKey: `library-distill:${payload.documentId}:${payload.runId}:0`,
  });
}

async function scheduleExtract(
  deps: ExtractDependencies,
  payload: LibraryExtractPayload,
  attempt: number,
  delaySeconds: number,
): Promise<void> {
  const sequence = payload.sequence + 1;
  await deps.enqueue(
    QUEUE.LIBRARY_EXTRACT,
    { ...payload, sequence, attempt },
    {
      startAfter: delaySeconds,
      retryLimit: MAX_TRANSIENT_ATTEMPTS,
      retryDelay: 15,
      retryBackoff: true,
      singletonKey: `library-extract:${payload.documentId}:${payload.runId}:${sequence}`,
    },
  );
}

async function handleExtractionError(
  deps: ExtractDependencies,
  payload: LibraryExtractPayload,
  error: unknown,
): Promise<void> {
  const message = error instanceof Error ? error.message : String(error);
  if (error instanceof RetryableLibraryExtractionError && error.capacityBusy) {
    await scheduleExtract(deps, payload, payload.attempt ?? 0, 30);
    return;
  }
  const terminal = error instanceof TerminalLibraryExtractionError;
  const attempt = payload.attempt ?? 0;
  if (!terminal && attempt + 1 < MAX_TRANSIENT_ATTEMPTS) {
    const nextAttempt = attempt + 1;
    await scheduleExtract(
      deps,
      payload,
      nextAttempt,
      Math.min(300, 5 * 2 ** attempt),
    );
    console.warn(
      `[library-extract] document ${payload.documentId} retry ${nextAttempt}/${MAX_TRANSIENT_ATTEMPTS - 1}: ${message}`,
    );
    return;
  }
  await deps.markStatus({
    orgId: payload.orgId,
    id: payload.documentId,
    status: "failed",
    error: message.slice(0, 2_000),
  });
  console.error(`[library-extract] document ${payload.documentId} failed: ${message}`);
}
