import "server-only";

import { mkdir, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { basename, extname, join, resolve } from "node:path";
import { ensureOrgWorkspace } from "@neko/llm";

const MIME_BY_EXT: Record<string, string> = {
  ".csv": "text/csv; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".md": "text/markdown; charset=utf-8",
  ".pdf": "application/pdf",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".txt": "text/plain; charset=utf-8",
  ".tsv": "text/tab-separated-values; charset=utf-8",
  ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
};

export function safeFileName(name: string): string {
  return basename(name).replace(/[^a-zA-Z0-9._-]/g, "_");
}

export async function saveWorkUpload(
  orgId: string,
  threadId: string,
  file: File,
): Promise<{ relativePath: string; absolutePath: string; name: string }> {
  const roots = await ensureOrgWorkspace(orgId);
  const dir = join(roots.uploadsRoot, threadId);
  await mkdir(dir, { recursive: true });
  const safeName = safeFileName(file.name || "upload.bin");
  const absolutePath = join(dir, safeName);
  const buffer = Buffer.from(await file.arrayBuffer());
  await writeFile(absolutePath, buffer);
  return {
    relativePath: join("uploads", threadId, safeName),
    absolutePath,
    name: safeName,
  };
}

export function joinMessageWithAttachments(
  text: string,
  attachments: Array<{ relativePath: string; absolutePath: string }>,
): string {
  if (attachments.length === 0) return text.trim();
  const prefix = text.trim();
  const lines = attachments.map((file) => `- ${file.relativePath} (${file.absolutePath})`);
  return [
    prefix,
    prefix ? "" : "",
    `I've attached ${attachments.length === 1 ? "a file" : "files"}:`,
    ...lines,
  ]
    .filter((line, index, arr) => !(line === "" && arr[index - 1] === ""))
    .join("\n");
}

export async function readWorkFile(orgId: string, relativePath: string): Promise<{
  data: Buffer;
  mimeType: string;
  filename: string;
}> {
  const roots = await ensureOrgWorkspace(orgId);
  const normalized = relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  if (
    !normalized.startsWith("uploads/") &&
    !normalized.startsWith("runs/") &&
    !normalized.startsWith("skills/") &&
    !normalized.startsWith("memory/")
  ) {
    throw new Error("Unsupported work file path.");
  }
  const absolute = resolve(roots.orgRoot, normalized);
  if (!absolute.startsWith(resolve(roots.orgRoot))) {
    throw new Error("Work file path escapes the org workspace.");
  }
  const [buffer, meta] = await Promise.all([readFile(absolute), stat(absolute)]);
  if (!meta.isFile()) throw new Error("Work file is not a regular file.");
  const ext = extname(absolute).toLowerCase();
  return {
    data: buffer,
    mimeType: MIME_BY_EXT[ext] ?? "application/octet-stream",
    filename: basename(absolute),
  };
}

export async function listWorkAssets(orgId: string): Promise<{
  skills: Array<{ name: string; path: string }>;
  memory: Array<{ name: string; path: string }>;
}> {
  const roots = await ensureOrgWorkspace(orgId);
  const [skillEntries, memoryEntries] = await Promise.all([
    readdir(roots.skillsRoot, { withFileTypes: true }).catch(() => []),
    readdir(roots.memoryRoot, { withFileTypes: true }).catch(() => []),
  ]);

  const skills = skillEntries
    .filter((entry) => entry.isDirectory())
    .map((entry) => ({
      name: entry.name,
      path: `/api/work/files/skills/${entry.name}/SKILL.md`,
    }))
    .sort((a, b) => a.name.localeCompare(b.name));

  const memory = memoryEntries
    .filter((entry) => entry.isFile() && entry.name.endsWith(".md"))
    .map((entry) => ({
      name: entry.name,
      path: `/api/work/files/memory/${entry.name}`,
    }))
    .sort((a, b) => a.name.localeCompare(b.name));

  return { skills, memory };
}
