// Durable extraction primitives for the Library worker. Markdown and plain
// text are read deterministically in-process. PDF/Office/CSV files are sent to
// the OpenNeko-owned librarian through its asynchronous task API with OCR and
// image enrichment explicitly disabled.

import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, rmdir, writeFile } from "node:fs/promises";
import { dirname, join, resolve, sep } from "node:path";
import { getOrgAgentRoot } from "../work/workspace";
import { doclingInputFormat } from "./formats";

const LIBRARIAN_REQUEST_TIMEOUT_MS = 150_000;
export const LIBRARY_EXTRACTION_OPTIONS_VERSION = "digital-text-v1";

export class RetryableLibraryExtractionError extends Error {
  constructor(message: string, readonly taskMissing = false, readonly capacityBusy = false) {
    super(message);
    this.name = "RetryableLibraryExtractionError";
  }
}

export class TerminalLibraryExtractionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TerminalLibraryExtractionError";
  }
}

/** One heading-delimited region of an extracted Markdown document. */
export type ExtractedSection = {
  /**
   * Heading trail from the document root to this section, self last
   * (e.g. ["Master Agreement", "3. Payment terms"]). Empty for the
   * preamble before the first heading.
   */
  headingPath: string[];
  /** Character offset of the section start in the returned text. */
  start: number;
  /** Character offset of the section end (exclusive). */
  end: number;
};

export type ExtractOutcome =
  | {
      ok: true;
      /** Structured Markdown from Docling. */
      text: string;
      /** Heading boundaries of `text`, ready to feed structure-aware chunking. */
      structure: { format: "markdown"; sections: ExtractedSection[] };
    }
  | { ok: false; reason: string };

export type LibrarianTaskState = "pending" | "started" | "success" | "failure";

export type LibrarianTaskStatus = {
  taskId: string;
  state: LibrarianTaskState;
  error: string | null;
};

/**
 * Base URL of the OpenNeko librarian extraction service, or null when
 * it isn't configured. The library requires it — a null here makes extraction
 * fail loudly rather than fall back.
 */
export function librarianServiceUrl(): string | null {
  const raw = process.env.NEKO_LIBRARIAN_URL?.trim();
  return raw ? raw.replace(/\/+$/, "") : null;
}

export function libraryExtractorFingerprint(kind: "text" | "docling"): string {
  if (kind === "text") return `openneko-text:${LIBRARY_EXTRACTION_OPTIONS_VERSION}`;
  return (
    process.env.NEKO_LIBRARIAN_FINGERPRINT?.trim() ||
    `neko-librarian:dev:${LIBRARY_EXTRACTION_OPTIONS_VERSION}`
  );
}

export function resolveLibrarySourcePath(
  orgId: string,
  relativePath: string,
): string {
  const orgRoot = resolve(getOrgAgentRoot(orgId));
  const normalized = relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  const absolutePath = resolve(join(orgRoot, normalized));
  assertPathInside(orgRoot, absolutePath);
  if (absolutePath === orgRoot) throw new Error("library source path is not a file");
  return absolutePath;
}

type DoclingConvertResponse = {
  document?: { md_content?: string | null };
  status?: string;
  errors?: unknown[];
};

type DoclingTaskResponse = {
  task_id?: unknown;
  task_status?: unknown;
  error?: unknown;
  error_message?: unknown;
};

/**
 * Submit extraction to the librarian service. The returned task id is stored
 * on the document row before a separate durable poll job is scheduled.
 */
export async function submitLibrarianExtraction(input: {
  absolutePath: string;
  filename: string;
  baseUrl: string;
}): Promise<string> {
  const format = doclingInputFormat(input.filename);
  if (!format) {
    throw new TerminalLibraryExtractionError(
      `unsupported librarian input "${input.filename}"`,
    );
  }
  const bytes = await readFile(input.absolutePath);
  const form = new FormData();
  form.append("files", new Blob([new Uint8Array(bytes)]), input.filename);
  form.append("from_formats", format);
  form.append("to_formats", "md");
  form.append("image_export_mode", "placeholder");
  form.append("do_ocr", "false");
  form.append("force_ocr", "false");
  form.append("do_table_structure", "true");
  form.append("include_images", "false");
  form.append("include_page_images", "false");
  form.append("do_picture_classification", "false");
  form.append("do_picture_description", "false");
  form.append("do_chart_extraction", "false");
  form.append("do_code_enrichment", "false");
  form.append("do_formula_enrichment", "false");
  form.append("abort_on_error", "true");
  const data = await requestLibrarianJson<DoclingTaskResponse>(
    `${input.baseUrl}/v1/convert/file/async`,
    { method: "POST", body: form },
  );
  if (typeof data.task_id !== "string" || data.task_id.length === 0) {
    throw new RetryableLibraryExtractionError(
      "librarian accepted extraction without returning a task id",
    );
  }
  return data.task_id;
}

export async function pollLibrarianExtraction(
  baseUrl: string,
  taskId: string,
): Promise<LibrarianTaskStatus> {
  const data = await requestLibrarianJson<DoclingTaskResponse>(
    `${baseUrl}/v1/status/poll/${encodeURIComponent(taskId)}`,
    undefined,
    true,
  );
  const state = data.task_status;
  if (!isTaskState(state)) {
    throw new RetryableLibraryExtractionError(
      `librarian returned an unknown task status for ${taskId}`,
    );
  }
  const error =
    typeof data.error_message === "string"
      ? data.error_message
      : typeof data.error === "string"
        ? data.error
        : null;
  return { taskId, state, error };
}

export async function fetchLibrarianExtraction(
  baseUrl: string,
  taskId: string,
): Promise<ExtractOutcome> {
  const data = await requestLibrarianJson<DoclingConvertResponse>(
    `${baseUrl}/v1/result/${encodeURIComponent(taskId)}`,
    undefined,
    true,
  );
  const markdown = data.document?.md_content ?? "";
  if (data.status !== "success") {
    const detail = summarizeDoclingErrors(data.errors);
    throw new TerminalLibraryExtractionError(
      `librarian conversion ${data.status ?? "failed"}${detail ? `: ${detail}` : ""}`,
    );
  }
  if (!markdown.trim()) {
    throw new TerminalLibraryExtractionError(
      "No embedded text was found. Scanned and handwritten documents are not supported yet.",
    );
  }
  return {
    ok: true,
    text: markdown,
    structure: { format: "markdown", sections: sliceMarkdownSections(markdown) },
  };
}

export async function extractDirectText(absolutePath: string): Promise<ExtractOutcome> {
  const raw = await readFile(absolutePath);
  if (raw.includes(0)) {
    throw new TerminalLibraryExtractionError("Text document contains binary data.");
  }
  const utf8 = raw.toString("utf8");
  const replacements = (utf8.match(/�/g) ?? []).length;
  const decoded =
    replacements > 0 && replacements > utf8.length / 200
      ? raw.toString("latin1")
      : utf8;
  const text = decoded.charCodeAt(0) === 0xfeff ? decoded.slice(1) : decoded;
  if (!text.trim()) {
    throw new TerminalLibraryExtractionError("Text document is empty.");
  }
  return {
    ok: true,
    text,
    structure: { format: "markdown", sections: sliceMarkdownSections(text) },
  };
}

export async function writeLibraryDerivedMarkdown(input: {
  orgId: string;
  documentId: string;
  extractorFingerprint: string;
  text: string;
}): Promise<{ relativePath: string; contentHash: string }> {
  const orgRoot = resolve(getOrgAgentRoot(input.orgId));
  const contentHash = hashLibraryText(input.text);
  const fingerprintHash = createHash("sha256")
    .update(input.extractorFingerprint)
    .digest("hex")
    .slice(0, 16);
  const relativePath = join(
    "library",
    "derived",
    input.documentId,
    fingerprintHash,
    `${contentHash}.md`,
  ).replace(/\\/g, "/");
  const absolutePath = resolve(join(orgRoot, relativePath));
  assertPathInside(orgRoot, absolutePath);
  await mkdir(dirname(absolutePath), { recursive: true, mode: 0o700 });
  const temporaryPath = `${absolutePath}.tmp-${process.pid}-${randomUUID()}`;
  try {
    await writeFile(temporaryPath, input.text, { encoding: "utf8", mode: 0o600 });
    await rename(temporaryPath, absolutePath);
  } finally {
    await rm(temporaryPath, { force: true });
  }
  return { relativePath, contentHash };
}

export async function readLibraryDerivedMarkdown(input: {
  orgId: string;
  relativePath: string;
  expectedHash: string;
}): Promise<ExtractOutcome> {
  const orgRoot = resolve(getOrgAgentRoot(input.orgId));
  const normalized = input.relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  if (!normalized.startsWith("library/derived/")) {
    throw new Error("refusing to read a non-derived library path");
  }
  const absolutePath = resolve(join(orgRoot, normalized));
  assertPathInside(orgRoot, absolutePath);
  const text = await readFile(absolutePath, "utf8");
  if (hashLibraryText(text) !== input.expectedHash) {
    throw new RetryableLibraryExtractionError(
      "prepared library extraction no longer matches its recorded hash",
    );
  }
  return {
    ok: true,
    text,
    structure: { format: "markdown", sections: sliceMarkdownSections(text) },
  };
}

export async function removeLibraryDerivedMarkdown(
  orgId: string,
  relativePath: string | null,
): Promise<void> {
  if (!relativePath) return;
  const normalized = relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  if (!normalized.startsWith("library/derived/")) {
    throw new Error("refusing to remove a non-derived library path");
  }
  const orgRoot = resolve(getOrgAgentRoot(orgId));
  const absolutePath = resolve(join(orgRoot, normalized));
  assertPathInside(orgRoot, absolutePath);
  await rm(absolutePath, { force: true });
  await rmdir(dirname(absolutePath)).catch(() => undefined);
  await rmdir(dirname(dirname(absolutePath))).catch(() => undefined);
}

export function hashLibraryText(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex");
}

async function requestLibrarianJson<T>(
  url: string,
  init?: RequestInit,
  taskLookup = false,
): Promise<T> {
  let response: Response;
  try {
    response = await fetch(url, {
      ...init,
      signal: AbortSignal.timeout(LIBRARIAN_REQUEST_TIMEOUT_MS),
    });
  } catch (error) {
    throw new RetryableLibraryExtractionError(
      `librarian is unavailable: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  if (taskLookup && response.status === 404) {
    throw new RetryableLibraryExtractionError(
      "librarian task disappeared after a service restart",
      true,
    );
  }
  if (response.status === 429) {
    throw new RetryableLibraryExtractionError("librarian capacity is full", false, true);
  }
  if (response.status >= 500) {
    throw new RetryableLibraryExtractionError(
      `librarian service returned HTTP ${response.status}`,
    );
  }
  if (!response.ok) {
    throw new TerminalLibraryExtractionError(
      `librarian rejected the document with HTTP ${response.status}`,
    );
  }
  try {
    return (await response.json()) as T;
  } catch {
    throw new RetryableLibraryExtractionError("librarian returned invalid JSON");
  }
}

function isTaskState(value: unknown): value is LibrarianTaskState {
  return value === "pending" || value === "started" || value === "success" || value === "failure";
}

function summarizeDoclingErrors(errors: unknown[] | undefined): string {
  if (!errors?.length) return "";
  return errors
    .slice(0, 3)
    .map((error) =>
      typeof error === "string"
        ? error
        : error && typeof error === "object" && "error_message" in error
          ? String((error as { error_message: unknown }).error_message)
          : "conversion error",
    )
    .join("; ");
}

function assertPathInside(root: string, candidate: string): void {
  if (candidate !== root && !candidate.startsWith(`${root}${sep}`)) {
    throw new Error("library path escapes the org workspace");
  }
}

const ATX_HEADING = /^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$/;
const FENCE = /^(```+|~~~+)/;

/**
 * Split extracted Markdown into heading-delimited sections with character
 * offsets and an ancestor heading trail. Pure and dependency-free so the
 * distiller's structure-aware chunking can group concepts by section without
 * the service running. Headings inside fenced code blocks are ignored.
 */
export function sliceMarkdownSections(markdown: string): ExtractedSection[] {
  type Boundary = { offset: number; level: number; title: string };
  const boundaries: Boundary[] = [];
  let offset = 0;
  let inFence = false;
  let fenceChar = "";
  for (const line of markdown.split("\n")) {
    const fence = FENCE.exec(line.trimStart());
    if (fence) {
      if (!inFence) {
        inFence = true;
        fenceChar = fence[1][0];
      } else if (line.trimStart().startsWith(fenceChar)) {
        inFence = false;
      }
    } else if (!inFence) {
      const m = ATX_HEADING.exec(line);
      if (m) boundaries.push({ offset, level: m[1].length, title: m[2].trim() });
    }
    offset += line.length + 1; // +1 for the "\n" removed by split
  }

  const total = markdown.length;
  const sections: ExtractedSection[] = [];
  const firstStart = boundaries.length > 0 ? boundaries[0].offset : total;
  if (firstStart > 0 && markdown.slice(0, firstStart).trim().length > 0) {
    sections.push({ headingPath: [], start: 0, end: firstStart });
  }
  const stack: Boundary[] = [];
  for (let i = 0; i < boundaries.length; i++) {
    const b = boundaries[i];
    while (stack.length > 0 && stack[stack.length - 1].level >= b.level) stack.pop();
    stack.push(b);
    const end = i + 1 < boundaries.length ? boundaries[i + 1].offset : total;
    sections.push({ headingPath: stack.map((s) => s.title), start: b.offset, end });
  }
  if (sections.length === 0) sections.push({ headingPath: [], start: 0, end: total });
  return sections;
}
