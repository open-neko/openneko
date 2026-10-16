import { createHash } from "node:crypto";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fingerprintSkillTree } from "./workspace";

export const SKILL_OVERLAYS_DIR = "skill-overlays";
export const LEARNED_FILE = "LEARNED.md";
export const LEARNED_START = "<!-- openneko-learned:start -->";
export const LEARNED_END = "<!-- openneko-learned:end -->";

export const CITE_RULES_INSTRUCTION =
  "When you apply a learned rule, name its id in one short line of your answer: applied learned rule: <id>.";
export const DESCRIPTION_DIR = "description";
export const DESCRIPTION_FILE = "DESCRIPTION.md";

export type LearnedOverlayStatus = "applied" | "stale" | "disabled";

/** A learned routing description; it replaces only the frontmatter description. */
export type LearnedDescription = {
  skillName: string;
  baseHash: string;
  status: LearnedOverlayStatus;
  description: string;
};

export type LearnedOverlay = {
  skillName: string;
  baseHash: string;
  status: LearnedOverlayStatus;
  learnEventId?: string;
  body: string;
};

export function skillOverlayDir(orgRoot: string, skillName: string): string {
  return join(orgRoot, SKILL_OVERLAYS_DIR, skillName);
}

export async function readLearnedOverlay(
  orgRoot: string,
  skillName: string,
): Promise<LearnedOverlay | null> {
  try {
    const raw = await readFile(
      join(skillOverlayDir(orgRoot, skillName), LEARNED_FILE),
      "utf8",
    );
    return parseLearnedMarkdown(raw, skillName);
  } catch {
    return null;
  }
}

export async function writeLearnedOverlay(
  orgRoot: string,
  overlay: LearnedOverlay,
): Promise<string> {
  const dir = skillOverlayDir(orgRoot, overlay.skillName);
  await mkdir(dir, { recursive: true });
  const path = join(dir, LEARNED_FILE);
  await writeFile(path, renderLearnedMarkdown(overlay), "utf8");
  return path;
}

export function skillDescriptionDir(orgRoot: string, skillName: string): string {
  return join(skillOverlayDir(orgRoot, skillName), DESCRIPTION_DIR);
}

export async function readLearnedDescription(
  orgRoot: string,
  skillName: string,
): Promise<LearnedDescription | null> {
  try {
    const raw = await readFile(join(skillDescriptionDir(orgRoot, skillName), DESCRIPTION_FILE), "utf8");
    const overlay = parseLearnedMarkdown(raw, skillName);
    if (!overlay || !overlay.body || /[\r\n"\\]/.test(overlay.body)) return null;
    return {
      skillName: overlay.skillName,
      baseHash: overlay.baseHash,
      status: overlay.status,
      description: overlay.body,
    };
  } catch {
    return null;
  }
}

export async function writeLearnedDescription(
  orgRoot: string,
  learned: LearnedDescription,
): Promise<string> {
  const dir = skillDescriptionDir(orgRoot, learned.skillName);
  await mkdir(dir, { recursive: true });
  const path = join(dir, DESCRIPTION_FILE);
  await writeFile(
    path,
    renderLearnedMarkdown({
      skillName: learned.skillName,
      baseHash: learned.baseHash,
      status: learned.status,
      body: learned.description,
    }),
    "utf8",
  );
  return path;
}

/** Replaces the frontmatter description, including folded continuation lines; the body is untouched. */
export function replaceFrontmatterDescription(skillMarkdown: string, description: string): string {
  const match = skillMarkdown.match(/^(---[ \t]*\r?\n)([\s\S]*?)(\r?\n---[ \t]*(?:\r?\n|$))/);
  if (!match) return skillMarkdown;
  const lines = match[2]!.split(/\r?\n/);
  const out: string[] = [];
  let replaced = false;
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index]!;
    if (!replaced && /^description\s*:/.test(line)) {
      out.push(`description: "${description}"`);
      replaced = true;
      while (index + 1 < lines.length && isContinuation(lines, index + 1)) index++;
      continue;
    }
    out.push(line);
  }
  if (!replaced) out.push(`description: "${description}"`);
  return `${match[1]}${out.join("\n")}${match[3]}${skillMarkdown.slice(match[0].length)}`;
}

function isContinuation(lines: readonly string[], index: number): boolean {
  const line = lines[index]!;
  if (/^\s+\S/.test(line)) return true;
  if (line.trim() !== "") return false;
  const next = lines.slice(index + 1).find((candidate) => candidate.trim() !== "");
  return next !== undefined && /^\s+\S/.test(next);
}

export function stripLearnedSection(skillMarkdown: string): string {
  const start = skillMarkdown.indexOf(LEARNED_START);
  if (start < 0) return skillMarkdown.trimEnd();
  const end = skillMarkdown.indexOf(LEARNED_END, start);
  if (end < 0) return skillMarkdown.slice(0, start).trimEnd();
  return `${skillMarkdown.slice(0, start).trimEnd()}\n${skillMarkdown
    .slice(end + LEARNED_END.length)
    .trimStart()}`.trimEnd();
}

export function appendLearnedSection(skillMarkdown: string, body: string): string {
  const base = stripLearnedSection(skillMarkdown).trimEnd();
  const guidance = body.trim();
  if (!guidance) return base;
  const cite = /^- \[skill:/m.test(guidance) ? `\n\n${CITE_RULES_INSTRUCTION}` : "";
  return `${base}\n\n${LEARNED_START}\n## Learned guidance\n\n${guidance}${cite}\n${LEARNED_END}\n`;
}

export async function overlayAppliesToBase(
  overlay: LearnedOverlay | null,
  baseHash: string,
): Promise<boolean> {
  if (!overlay) return false;
  if (overlay.status === "disabled") return false;
  return overlay.status === "applied" && overlay.baseHash === baseHash;
}

export function descriptionAppliesToBase(
  learned: LearnedDescription | null,
  baseHash: string,
): boolean {
  return Boolean(learned && learned.status === "applied" && learned.baseHash === baseHash);
}

export async function fingerprintEffectiveSkill(
  orgRoot: string,
  skillName: string,
  baseDir: string,
): Promise<string> {
  const baseHash = await fingerprintSkillTree(baseDir);
  const overlay = await readLearnedOverlay(orgRoot, skillName);
  const learned = await readLearnedDescription(orgRoot, skillName);
  const rulesLive = await overlayAppliesToBase(overlay, baseHash);
  const descriptionLive = descriptionAppliesToBase(learned, baseHash);
  if (!rulesLive && !descriptionLive) return baseHash;
  const hash = createHash("sha256").update(baseHash).update("\0").update(rulesLive ? overlay!.body : "");
  if (descriptionLive) hash.update("\0description\0").update(learned!.description);
  return hash.digest("hex");
}

export async function composeSkillTree(input: {
  baseDir: string;
  destDir: string;
  overlay: LearnedOverlay | null;
  description?: LearnedDescription | null;
}): Promise<"applied" | "base-only"> {
  await rm(input.destDir, { recursive: true, force: true });
  await mkdir(input.destDir, { recursive: true });
  await cp(input.baseDir, input.destDir, { recursive: true, force: true });
  const baseHash = await fingerprintSkillTree(input.baseDir);
  const rulesLive = await overlayAppliesToBase(input.overlay, baseHash);
  const descriptionLive = descriptionAppliesToBase(input.description ?? null, baseHash);
  if (!rulesLive && !descriptionLive) return "base-only";
  const skillPath = join(input.destDir, "SKILL.md");
  let current = await readFile(skillPath, "utf8");
  if (descriptionLive) current = replaceFrontmatterDescription(current, input.description!.description);
  if (rulesLive) current = appendLearnedSection(current, input.overlay!.body);
  await writeFile(skillPath, current, "utf8");
  return "applied";
}

/**
 * Sandboxed runs get composed skills from staging. An unsandboxed run reads the
 * org skills folder directly, so live overlays are composed into a temporary
 * copy here. Returns null when no overlay is live.
 */
export async function composeLiveOverlays(
  skillsRoot: string,
): Promise<{ root: string; cleanup: () => Promise<void> } | null> {
  const orgRoot = dirname(skillsRoot);
  let overlayNames: string[];
  try {
    overlayNames = (await readdir(join(orgRoot, SKILL_OVERLAYS_DIR), { withFileTypes: true }))
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name);
  } catch {
    return null;
  }
  const live = new Map<string, { overlay: LearnedOverlay | null; description: LearnedDescription | null }>();
  for (const name of overlayNames) {
    const overlay = await readLearnedOverlay(orgRoot, name);
    const description = await readLearnedDescription(orgRoot, name);
    if (!overlay && !description) continue;
    const baseHash = await fingerprintSkillTree(join(skillsRoot, name)).catch(() => "");
    if (!baseHash) continue;
    const rulesLive = await overlayAppliesToBase(overlay, baseHash);
    const descriptionLive = descriptionAppliesToBase(description, baseHash);
    if (rulesLive || descriptionLive) {
      live.set(name, { overlay: rulesLive ? overlay : null, description: descriptionLive ? description : null });
    }
  }
  if (live.size === 0) return null;

  const scratch = await mkdtemp(join(tmpdir(), "neko-skills-"));
  const root = join(scratch, "skills");
  await mkdir(root, { recursive: true });
  for (const entry of await readdir(skillsRoot, { withFileTypes: true })) {
    const source = join(skillsRoot, entry.name);
    const learned = live.get(entry.name);
    if (learned) {
      await composeSkillTree({ baseDir: source, destDir: join(root, entry.name), ...learned });
    } else {
      await symlink(source, join(root, entry.name));
    }
  }
  return { root, cleanup: () => rm(scratch, { recursive: true, force: true }) };
}

function parseLearnedMarkdown(
  raw: string,
  fallbackName: string,
): LearnedOverlay | null {
  const match = raw.match(/^---\s*\n([\s\S]*?)\n---\s*\n?([\s\S]*)$/);
  if (!match) return null;
  const fm = match[1] ?? "";
  const body = (match[2] ?? "").trim();
  const baseHash = scalar(fm, "base_hash");
  if (!baseHash) return null;
  const status = scalar(fm, "status") ?? "applied";
  if (status !== "applied" && status !== "stale" && status !== "disabled") {
    return null;
  }
  return {
    skillName: scalar(fm, "skill_name") || fallbackName,
    baseHash,
    status,
    ...(scalar(fm, "learn_event_id")
      ? { learnEventId: scalar(fm, "learn_event_id") }
      : {}),
    body,
  };
}

function renderLearnedMarkdown(overlay: LearnedOverlay): string {
  const lines = [
    "---",
    `skill_name: ${overlay.skillName}`,
    `base_hash: ${overlay.baseHash}`,
    `status: ${overlay.status}`,
  ];
  if (overlay.learnEventId) lines.push(`learn_event_id: ${overlay.learnEventId}`);
  lines.push("---", "", overlay.body.trim(), "");
  return lines.join("\n");
}

function scalar(frontmatter: string, key: string): string | undefined {
  const match = frontmatter.match(new RegExp(`^${key}:\\s*(.+)$`, "m"));
  return match?.[1]?.trim();
}
