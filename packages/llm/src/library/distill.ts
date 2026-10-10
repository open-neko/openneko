// The librarian: distill one library_document into OKF concepts on the
// uploader's personal layer. Extraction is a preceding durable stage; this
// stage consumes its normalized Markdown and sends every chunk to the model.
// The model call and text read are injectable so tests run without a provider.

import type { HarnessObserver } from "@neko/telemetry";
import { observeAgentJob } from "../agent-job-telemetry";
import {
  clearLibraryDistillCheckpoint,
  clearLibraryTransientState,
  getLibraryDistillCheckpoint,
  getLibraryDocument,
  listLibraryConcepts,
  markLibraryDocumentStatus,
  saveLibraryDistillCheckpoint,
  upsertLibraryConcept,
  type LibraryConcept,
} from "../work/library";
import {
  hashLibraryText,
  readLibraryDerivedMarkdown,
  removeLibraryDerivedMarkdown,
  resolveLibrarySourcePath,
  type ExtractOutcome,
} from "./extract";
import { planDocumentChunks } from "./chunk";
import {
  extractLibraryFences,
  parseLibraryUpsert,
  type LibraryUpsertOp,
} from "./fence";
import { buildDistillPrompt } from "./prompts";

export type DistillLlm = (prompt: string) => Promise<string>;
export type DistillExtract = (input: {
  absolutePath: string;
  filename: string;
}) => Promise<ExtractOutcome>;

export type LibraryDistillResult = {
  status: "cataloged" | "skipped";
  reason?: string;
  concepts: LibraryConcept[];
};

async function defaultLlm(orgId: string): Promise<DistillLlm> {
  const [{ ax }, { buildLlm }] = await Promise.all([
    import("@ax-llm/ax"),
    import("../llm"),
  ]);
  const llm = await buildLlm(orgId);
  const librarian = ax(
    `librarianPrompt:string "librarian instructions plus one uploaded document" -> libraryOperations:string "a single neko_library fenced JSON array of flat objects discriminated by op"`,
    {
      description:
        "You are a meticulous librarian. Follow the instructions in the prompt exactly and respond with only the fenced neko_library block.",
    },
  );
  return async (prompt: string) => {
    const result = await librarian.forward(llm, { librarianPrompt: prompt });
    return String(result.libraryOperations ?? "");
  };
}

type LibraryDistillInput = {
  orgId: string;
  documentId: string;
  /** Re-run an explicitly retried failed/skipped document. */
  force?: boolean;
  llm?: DistillLlm;
  extract?: DistillExtract;
  observer?: HarnessObserver;
};

export function runLibraryDistill(input: LibraryDistillInput): Promise<LibraryDistillResult> {
  return observeAgentJob(
    {
      observer: input.observer,
      operationId: `library-distill:${input.documentId}`,
      productPath: "library_distill",
    },
    () => distill(input),
  );
}

async function distill(input: LibraryDistillInput): Promise<LibraryDistillResult> {
  const { orgId, documentId } = input;
  const document = await getLibraryDocument(orgId, documentId);
  if (!document) throw new Error(`library document not found: ${documentId}`);
  if (
    !input.force &&
    (document.status === "cataloged" || document.status === "skipped")
  ) {
    return { status: document.status, concepts: [] };
  }
  await markLibraryDocumentStatus({ orgId, id: documentId, status: "distilling" });

  // Validate the citation source even though extraction reads its own protected
  // derived path. A malformed source must never be materialized into a concept.
  const absolutePath = resolveLibrarySourcePath(orgId, document.relativePath);

  const outcome = input.extract
    ? await input.extract({ absolutePath, filename: document.filename })
    : document.extractedRelativePath && document.extractedContentHash
      ? await readLibraryDerivedMarkdown({
          orgId,
          relativePath: document.extractedRelativePath,
          expectedHash: document.extractedContentHash,
        })
      : null;
  if (!outcome) {
    throw new Error("document has no prepared extraction; extraction must complete first");
  }
  if (!outcome.ok) throw new Error(outcome.reason);
  const content = outcome.text;

  const catalog = await listLibraryConcepts({ orgId, userId: document.userId });
  const catalogForPrompt = catalog.map((c) => ({
    path: c.path,
    title: c.title,
    description: c.description ?? undefined,
  }));

  const sections = outcome.structure.sections;
  const llm = input.llm ?? (await defaultLlm(orgId));
  const chunks = planDocumentChunks(content, sections);
  const extractedContentHash = hashLibraryText(content);
  const chunkPlanHash = hashLibraryText(
    JSON.stringify(
      chunks.map((chunk) => ({
        index: chunk.index,
        total: chunk.total,
        headingPath: chunk.headingPath,
        hash: hashLibraryText(chunk.text),
      })),
    ),
  );
  const resume = parseCheckpoint(await getLibraryDistillCheckpoint(orgId, documentId), {
    sourceContentHash: document.contentHash,
    extractorFingerprint: document.extractorFingerprint ?? "injected-test-extractor",
    extractedContentHash,
    chunkPlanHash,
    chunkTotal: chunks.length,
  });

  // Map: distill each chunk. Reduce: merge upsert ops by path across chunks
  // so a concept spanning parts is combined, not clobbered. Each chunk is
  // shown the concepts already emitted from this document so the model
  // reuses their paths (update-not-append, extended across parts).
  const merged = new Map<string, LibraryUpsertOp>();
  let startAt = 0;
  if (resume) {
    startAt = resume.cursor;
    for (const op of resume.ops) merged.set(op.path, op);
  } else {
    await clearLibraryDistillCheckpoint(orgId, documentId);
  }

  for (let i = startAt; i < chunks.length; i++) {
    const chunk = chunks[i];
    const prompt = buildDistillPrompt({
      filename: document.filename,
      content: chunk.text,
      catalog: catalogForPrompt,
      chunk: {
        index: chunk.index,
        total: chunk.total,
        headingPath: chunk.headingPath,
      },
      priorConcepts: [...merged.values()].map((op) => ({
        path: op.path,
        title: op.title,
      })),
    });
    const raw = await llm(prompt);
    const { ops } = extractLibraryFences(raw);
    const upserts = ops.filter(
      (op): op is LibraryUpsertOp => op.kind === "upsert",
    );
    if (upserts.length === 0) {
      throw new Error(
        `librarian returned no parseable neko_library upserts for chunk ${i + 1}`,
      );
    }
    for (const op of upserts) {
      const existing = merged.get(op.path);
      merged.set(op.path, existing ? mergeUpsertOps(existing, op) : op);
    }
    // Checkpoint after each chunk of a multi-chunk document so a failure on
    // a later chunk resumes here instead of re-billing this one.
    if (chunks.length > 1) {
      await saveLibraryDistillCheckpoint(orgId, documentId, {
        v: 2,
        sourceContentHash: document.contentHash,
        extractorFingerprint:
          document.extractorFingerprint ?? "injected-test-extractor",
        extractedContentHash,
        chunkPlanHash,
        chunkTotal: chunks.length,
        cursor: i + 1,
        ops: [...merged.values()],
      });
    }
  }

  if (merged.size === 0) {
    throw new Error("librarian returned no upsert operations");
  }

  const concepts: LibraryConcept[] = [];
  const now = new Date().toISOString();
  const generatedBy = await producerActor();
  for (const op of merged.values()) {
    const { concept } = await upsertLibraryConcept({
      orgId,
      userId: document.userId,
      path: op.path,
      type: op.type,
      title: op.title,
      description: op.description ?? null,
      tags: op.tags ?? [],
      body: op.body,
      sources: [{ resource: `/${document.relativePath}`, last_modified: now }],
      generatedBy,
      sourceDocumentId: document.id,
      ...(op.stale_after ? { staleAfter: op.stale_after } : {}),
    });
    concepts.push(concept);
  }
  // Commit the user-visible outcome before filesystem cleanup. The artifact
  // pointer stays durable until deletion, so the boot/periodic sweep can
  // finish this step if the process exits between these operations.
  await markLibraryDocumentStatus({ orgId, id: documentId, status: "cataloged" });
  try {
    await removeLibraryDerivedMarkdown(orgId, document.extractedRelativePath);
    await clearLibraryTransientState(orgId, documentId);
  } catch (error) {
    console.warn(
      `[library-distill] cataloged ${documentId}; deferred transient cleanup: ${error instanceof Error ? error.message : error}`,
    );
  }
  return { status: "cataloged", concepts };
}

type DistillCheckpoint = {
  v: 2;
  sourceContentHash: string;
  extractorFingerprint: string;
  extractedContentHash: string;
  chunkPlanHash: string;
  /** Number of chunks the recorded plan produced. */
  chunkTotal: number;
  /** Chunks already completed (resume starts here). */
  cursor: number;
  /** Merged upsert ops accumulated so far. */
  ops: LibraryUpsertOp[];
};

/**
 * Validate a stored checkpoint against the current extraction. Returns null
 * (start fresh) when it's missing, the wrong version, or references sections
 * that no longer exist — i.e. the document was re-extracted differently.
 */
function parseCheckpoint(
  raw: Record<string, unknown> | null,
  expected: Omit<DistillCheckpoint, "v" | "cursor" | "ops">,
): DistillCheckpoint | null {
  if (!raw || raw.v !== 2 || !Array.isArray(raw.ops)) return null;
  if (typeof raw.chunkTotal !== "number" || typeof raw.cursor !== "number") return null;
  if (
    raw.sourceContentHash !== expected.sourceContentHash ||
    raw.extractorFingerprint !== expected.extractorFingerprint ||
    raw.extractedContentHash !== expected.extractedContentHash ||
    raw.chunkPlanHash !== expected.chunkPlanHash ||
    raw.chunkTotal !== expected.chunkTotal ||
    !Number.isInteger(raw.cursor) ||
    raw.cursor < 0 ||
    raw.cursor > expected.chunkTotal
  ) return null;
  const ops = raw.ops.map(parseLibraryUpsert);
  if (ops.some((op) => op === null)) return null;
  return {
    v: 2,
    ...expected,
    cursor: raw.cursor,
    ops: ops as LibraryUpsertOp[],
  };
}

/**
 * Reduce step: combine two upsert ops for the same concept path emitted by
 * different chunks. Bodies are concatenated (skipping an exact duplicate),
 * tags unioned, and the more conservative (earlier) expiry kept. The
 * first-seen title/type win — the earliest chunk usually carries the
 * concept's primary framing.
 */
export function mergeUpsertOps(a: LibraryUpsertOp, b: LibraryUpsertOp): LibraryUpsertOp {
  const bodyA = a.body.trim();
  const bodyB = b.body.trim();
  return {
    kind: "upsert",
    path: a.path,
    type: a.type,
    title: a.title,
    description: a.description ?? b.description,
    tags: Array.from(new Set([...(a.tags ?? []), ...(b.tags ?? [])])),
    body: bodyA.includes(bodyB) ? bodyA : `${bodyA}\n\n${bodyB}`,
    stale_after: earlierDate(a.stale_after, b.stale_after),
  };
}

// ISO YYYY-MM-DD strings compare correctly lexicographically.
function earlierDate(a?: string, b?: string): string | undefined {
  if (!a) return b;
  if (!b) return a;
  return a <= b ? a : b;
}

// OKF actor convention: "<producer>/<version>".
async function producerActor(): Promise<string> {
  try {
    const { readFile: read } = await import("node:fs/promises");
    const { fileURLToPath } = await import("node:url");
    const { dirname, join: pjoin } = await import("node:path");
    const pkgPath = pjoin(
      dirname(fileURLToPath(import.meta.url)),
      "..",
      "..",
      "package.json",
    );
    const pkg = JSON.parse(await read(pkgPath, "utf8")) as { version?: string };
    return `openneko/${pkg.version ?? "dev"}`;
  } catch {
    return "openneko/dev";
  }
}
