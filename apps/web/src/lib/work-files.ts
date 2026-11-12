import "server-only";

import { readFile, readdir, stat } from "node:fs/promises";
import { basename, join } from "node:path";
import { ensureOrgWorkspace } from "@neko/llm/work";
import { LIBRARY_SUPPORTED_EXTENSIONS } from "@neko/llm/library/formats";

export const ALLOWED_UPLOAD_EXTENSIONS = new Set<string>(LIBRARY_SUPPORTED_EXTENSIONS);

export function safeFileName(name: string): string {
  const base = basename(name).split("\\").pop() ?? name;
  return base
    .replace(/[^a-zA-Z0-9._-]/g, "_")
    .replace(/^\.+/, "_")
    .slice(0, 200) || "upload.bin";
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

export type WorkSkillSummary = {
  name: string;
  description: string;
  fileCount: number;
  updatedAt: string;
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
