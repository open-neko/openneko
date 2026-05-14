import "server-only";

import { mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { basename, extname, join, resolve } from "node:path";
import { ensureOrgWorkspace } from "@neko/llm/work";

export const MAX_UPLOAD_SIZE = 10 * 1024 * 1024;

export const ALLOWED_UPLOAD_EXTENSIONS = new Set<string>([
  ".csv",
  ".docx",
  ".html",
  ".json",
  ".md",
  ".pdf",
  ".pptx",
  ".tsv",
  ".txt",
  ".xlsx",
]);

const MIME_BY_EXT: Record<string, string> = {
  ".csv": "text/csv; charset=utf-8",
  ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ".html": "text/html; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".md": "text/markdown; charset=utf-8",
  ".pdf": "application/pdf",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  ".txt": "text/plain; charset=utf-8",
  ".tsv": "text/tab-separated-values; charset=utf-8",
  ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
};

// Office docs browsers can't preview — the file-serving route uses this set
// to force download instead of trying (and failing) to render inline.
export const FORCE_DOWNLOAD_EXTENSIONS = new Set([".docx", ".pptx", ".xlsx"]);

export function safeFileName(name: string): string {
  const base = basename(name).split("\\").pop() ?? name;
  return base
    .replace(/[^a-zA-Z0-9._-]/g, "_")
    .replace(/^\.+/, "_")
    .slice(0, 200) || "upload.bin";
}

export async function saveWorkUpload(
  orgId: string,
  threadId: string,
  file: File,
): Promise<{ relativePath: string; absolutePath: string; name: string; size: number }> {
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
    size: buffer.byteLength,
  };
}

export function joinMessageWithAttachments(
  text: string,
  attachments: Array<{ relativePath: string; name: string; size?: number }>,
): string {
  const prefix = text.trim();
  if (attachments.length === 0) return prefix;
  const lines = attachments.map((file) => {
    const kb = typeof file.size === "number" ? `, ${Math.max(1, Math.round(file.size / 1024))} KB` : "";
    return `- ${file.relativePath}  (${file.name}${kb})`;
  });
  const header = `I've attached ${attachments.length === 1 ? "a file" : "files"}:`;
  return prefix ? `${prefix}\n\n${header}\n${lines.join("\n")}` : `${header}\n${lines.join("\n")}`;
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

export type WorkSkillSummary = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
};

export type WorkSkillDetail = WorkSkillSummary & {
  path: string;
  skillMarkdown: string;
  files: Array<{ path: string; bytes: number }>;
};

function parseSkillFrontmatter(markdown: string): { description: string } {
  const match = /^---\n([\s\S]*?)\n---/.exec(markdown);
  if (!match) return { description: "" };
  const block = match[1];
  const desc = /^description:\s*(.*)$/m.exec(block);
  if (!desc) return { description: "" };
  let value = desc[1].trim();
  if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
    value = value.slice(1, -1);
  }
  return { description: value };
}

async function listSkillFiles(skillDir: string): Promise<Array<{ path: string; bytes: number; mtime: number }>> {
  const out: Array<{ path: string; bytes: number; mtime: number }> = [];
  async function walk(dir: string, prefix: string) {
    const entries = await readdir(dir, { withFileTypes: true }).catch(() => []);
    for (const entry of entries) {
      const abs = join(dir, entry.name);
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) {
        await walk(abs, rel);
      } else if (entry.isFile()) {
        const st = await stat(abs).catch(() => null);
        if (!st) continue;
        out.push({ path: rel, bytes: st.size, mtime: st.mtimeMs });
      }
    }
  }
  await walk(skillDir, "");
  return out;
}

export async function listWorkSkills(orgId: string): Promise<WorkSkillSummary[]> {
  const roots = await ensureOrgWorkspace(orgId);
  const entries = await readdir(roots.skillsRoot, { withFileTypes: true }).catch(() => []);
  const dirs = entries.filter((e) => e.isDirectory());
  const summaries = await Promise.all(
    dirs.map(async (entry): Promise<WorkSkillSummary | null> => {
      const skillDir = join(roots.skillsRoot, entry.name);
      const files = await listSkillFiles(skillDir);
      if (files.length === 0) return null;
      const skillMd = await readFile(join(skillDir, "SKILL.md"), "utf8").catch(() => "");
      const { description } = parseSkillFrontmatter(skillMd);
      const latest = files.reduce((max, f) => (f.mtime > max ? f.mtime : max), 0);
      return {
        name: entry.name,
        description,
        fileCount: files.length,
        updatedAt: new Date(latest || Date.now()).toISOString(),
      };
    }),
  );
  return summaries
    .filter((s): s is WorkSkillSummary => s !== null)
    .sort((a, b) => a.name.localeCompare(b.name));
}

export async function deleteWorkSkill(orgId: string, name: string): Promise<boolean> {
  const safeName = basename(name);
  if (safeName !== name || !/^[a-zA-Z0-9._-]+$/.test(safeName)) return false;
  const roots = await ensureOrgWorkspace(orgId);
  const skillDir = join(roots.skillsRoot, safeName);
  const st = await stat(skillDir).catch(() => null);
  if (!st || !st.isDirectory()) return false;
  await rm(skillDir, { recursive: true, force: true });
  return true;
}

export async function getWorkSkillDetail(orgId: string, name: string): Promise<WorkSkillDetail | null> {
  const safeName = basename(name);
  if (safeName !== name || !/^[a-zA-Z0-9._-]+$/.test(safeName)) return null;
  const roots = await ensureOrgWorkspace(orgId);
  const skillDir = join(roots.skillsRoot, safeName);
  const files = await listSkillFiles(skillDir);
  if (files.length === 0) return null;
  const skillMarkdown = await readFile(join(skillDir, "SKILL.md"), "utf8").catch(() => "");
  const { description } = parseSkillFrontmatter(skillMarkdown);
  const latest = files.reduce((max, f) => (f.mtime > max ? f.mtime : max), 0);
  return {
    name: safeName,
    description,
    fileCount: files.length,
    updatedAt: new Date(latest || Date.now()).toISOString(),
    path: skillDir,
    skillMarkdown,
    files: files
      .map((f) => ({ path: f.path, bytes: f.bytes }))
      .sort((a, b) => a.path.localeCompare(b.path)),
  };
}
