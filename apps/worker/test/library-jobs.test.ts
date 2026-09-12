import { describe, expect, it, vi } from "vitest";
import type { LibraryExtractPayload, LibraryDistillPayload } from "@neko/db/jobs";
import type { LibraryDocument } from "@neko/llm";
import {
  libraryExtractorFingerprint,
  RetryableLibraryExtractionError,
  TerminalLibraryExtractionError,
} from "@neko/llm";
import { runLibraryDistillJob } from "../src/jobs/library-distill";
import { runLibraryExtractJob } from "../src/jobs/library-extract";
import { recoverUnqueuedLibraryUploads } from "../src/jobs/library-recovery";

const payload: LibraryExtractPayload = {
  orgId: "org-1",
  documentId: "doc-1",
  runId: "run-1",
  sequence: 0,
  attempt: 0,
};

function document(overrides: Partial<LibraryDocument> = {}): LibraryDocument {
  return {
    id: "doc-1",
    orgId: "org-1",
    userId: "user-1",
    sourceThreadId: null,
    filename: "policy.pdf",
    relativePath: "library/uploads/user-1/policy.pdf",
    contentHash: "source-hash",
    sizeBytes: 1_000,
    status: "uploaded",
    skipReason: null,
    error: null,
    extractCheckpoint: null,
    extractedRelativePath: null,
    extractedContentHash: null,
    extractorFingerprint: null,
    extractedAt: null,
    distilledAt: null,
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    ...overrides,
  };
}

function extractionDependencies(overrides: Record<string, unknown> = {}) {
  return {
    enqueue: vi.fn(async () => "job-1"),
    getDocument: vi.fn(async () => document()),
    getCheckpoint: vi.fn(async () => null),
    saveCheckpoint: vi.fn(async () => undefined),
    completeExtraction: vi.fn(async () => undefined),
    clearTransientState: vi.fn(async () => undefined),
    markStatus: vi.fn(async () => undefined),
    resolveSourcePath: vi.fn(() => "/tmp/policy.pdf"),
    serviceUrl: vi.fn(() => "http://librarian:5001"),
    submit: vi.fn(async () => "task-1"),
    poll: vi.fn(async () => ({ taskId: "task-1", state: "pending", error: null })),
    fetchResult: vi.fn(async () => ({
      ok: true as const,
      text: "# Policy\n\nAll text",
      structure: { format: "markdown" as const, sections: [] },
    })),
    extractText: vi.fn(async () => ({
      ok: true as const,
      text: "# Policy\n\nAll text",
      structure: { format: "markdown" as const, sections: [] },
    })),
    writeDerived: vi.fn(async () => ({
      relativePath: "library/derived/doc-1/fingerprint/content.md",
      contentHash: "extracted-hash",
    })),
    readDerived: vi.fn(async () => ({
      ok: true as const,
      text: "# Policy",
      structure: { format: "markdown" as const, sections: [] },
    })),
    removeDerived: vi.fn(async () => undefined),
    ...overrides,
  };
}

describe("runLibraryExtractJob", () => {
  it("queues capacity waits without exhausting transient retries", async () => {
    const deps = extractionDependencies({
      submit: vi.fn(async () => { throw new RetryableLibraryExtractionError("busy", false, true); }),
    });
    await runLibraryExtractJob({ ...payload, attempt: 7 }, deps);
    expect(deps.enqueue).toHaveBeenCalledWith("library_extract",
      expect.objectContaining({ attempt: 7 }), expect.objectContaining({ startAfter: 30 }));
    expect(deps.markStatus).not.toHaveBeenCalledWith(expect.objectContaining({ status: "failed" }));
  });
  it("extracts agent-generated Markdown in-process and queues distillation", async () => {
    const deps = extractionDependencies({
      getDocument: vi.fn(async () => document({ filename: "agent-note.md" })),
    });
    await runLibraryExtractJob(payload, deps);
    expect(deps.extractText).toHaveBeenCalledWith("/tmp/policy.pdf");
    expect(deps.submit).not.toHaveBeenCalled();
    expect(deps.completeExtraction).toHaveBeenCalledWith(
      expect.objectContaining({
        relativePath: "library/derived/doc-1/fingerprint/content.md",
        contentHash: "extracted-hash",
      }),
    );
    expect(deps.enqueue).toHaveBeenCalledWith(
      "library_distill",
      expect.objectContaining({ runId: "run-1", sequence: 0 }),
      expect.objectContaining({
        singletonKey: "library-distill:doc-1:run-1:0",
      }),
    );
  });

  it("checkpoints a new Docling task before scheduling its poll", async () => {
    const deps = extractionDependencies();
    await runLibraryExtractJob(payload, deps);
    expect(deps.submit).toHaveBeenCalledOnce();
    expect(deps.saveCheckpoint).toHaveBeenCalledWith(
      "org-1",
      "doc-1",
      expect.objectContaining({
        v: 1,
        sourceContentHash: "source-hash",
        taskId: "task-1",
      }),
    );
    expect(deps.enqueue).toHaveBeenCalledWith(
      "library_extract",
      expect.objectContaining({ sequence: 1, attempt: 0 }),
      expect.objectContaining({
        startAfter: 3,
        singletonKey: "library-extract:doc-1:run-1:1",
      }),
    );
  });

  it("resubmits when a librarian restart loses the in-memory task", async () => {
    const deps = extractionDependencies({
      getCheckpoint: vi.fn(async () => ({
        v: 1,
        sourceContentHash: "source-hash",
        extractorFingerprint: libraryExtractorFingerprint("docling"),
        taskId: "lost-task",
        pollCount: 2,
        submittedAt: new Date().toISOString(),
      })),
      poll: vi.fn(async () => {
        throw new RetryableLibraryExtractionError("task missing", true);
      }),
      submit: vi.fn(async () => "replacement-task"),
    });
    await runLibraryExtractJob(payload, deps);
    expect(deps.poll).toHaveBeenCalledWith(
      "http://librarian:5001",
      "lost-task",
    );
    expect(deps.submit).toHaveBeenCalledOnce();
    expect(deps.saveCheckpoint).toHaveBeenCalledWith(
      "org-1",
      "doc-1",
      expect.objectContaining({ taskId: "replacement-task", pollCount: 0 }),
    );
  });

  it("backs off transient failures but fails terminal document errors immediately", async () => {
    const retryDeps = extractionDependencies({
      submit: vi.fn(async () => {
        throw new RetryableLibraryExtractionError("service down");
      }),
    });
    await runLibraryExtractJob(payload, retryDeps);
    expect(retryDeps.enqueue).toHaveBeenCalledWith(
      "library_extract",
      expect.objectContaining({ attempt: 1 }),
      expect.objectContaining({ startAfter: 5 }),
    );
    expect(retryDeps.markStatus).toHaveBeenCalledWith(
      expect.objectContaining({ status: "extracting" }),
    );
    expect(retryDeps.markStatus).not.toHaveBeenCalledWith(
      expect.objectContaining({ status: "failed" }),
    );

    const terminalDeps = extractionDependencies({
      submit: vi.fn(async () => {
        throw new TerminalLibraryExtractionError("not a digital document");
      }),
    });
    await runLibraryExtractJob(payload, terminalDeps);
    expect(terminalDeps.enqueue).not.toHaveBeenCalled();
    expect(terminalDeps.markStatus).toHaveBeenLastCalledWith(
      expect.objectContaining({ status: "failed", error: "not a digital document" }),
    );
  });
});

describe("runLibraryDistillJob", () => {
  const distillPayload: LibraryDistillPayload = payload;

  it("schedules a durable retry when one chunk is malformed", async () => {
    const enqueue = vi.fn(async () => "job-2");
    const markStatus = vi.fn(async () => undefined);
    await runLibraryDistillJob(distillPayload, {
      run: vi.fn(async () => {
        throw new Error("no parseable upserts for chunk 2");
      }),
      enqueue,
      markStatus,
    });
    expect(enqueue).toHaveBeenCalledWith(
      "library_distill",
      expect.objectContaining({ attempt: 1, sequence: 1 }),
      expect.objectContaining({ startAfter: 15 }),
    );
    expect(markStatus).not.toHaveBeenCalled();
  });

  it("retains checkpoints and records failure after bounded retries", async () => {
    const enqueue = vi.fn(async () => "unused");
    const markStatus = vi.fn(async () => undefined);
    await runLibraryDistillJob(
      { ...distillPayload, attempt: 4, sequence: 4 },
      {
        run: vi.fn(async () => {
          throw new Error("provider still malformed");
        }),
        enqueue,
        markStatus,
      },
    );
    expect(enqueue).not.toHaveBeenCalled();
    expect(markStatus).toHaveBeenCalledWith(
      expect.objectContaining({ status: "failed", error: "provider still malformed" }),
    );
  });
});

describe("recoverUnqueuedLibraryUploads", () => {
  it("re-enqueues committed uploads that never crossed the queue boundary", async () => {
    const list = vi.fn(async () => [
      { orgId: "org-1", documentId: "doc-stranded" },
    ]);
    const enqueue = vi.fn(async () => "job-recovered");
    const now = new Date("2026-09-04T12:00:00.000Z");

    await expect(
      recoverUnqueuedLibraryUploads(now, { list, enqueue }),
    ).resolves.toBe(1);
    expect(list).toHaveBeenCalledWith(new Date("2026-09-04T11:55:00.000Z"));
    expect(enqueue).toHaveBeenCalledWith(
      "library_extract",
      expect.objectContaining({
        orgId: "org-1",
        documentId: "doc-stranded",
        runId: expect.any(String),
        sequence: 0,
      }),
      expect.objectContaining({ singletonKey: "library-extract:doc-stranded" }),
    );
  });
});
