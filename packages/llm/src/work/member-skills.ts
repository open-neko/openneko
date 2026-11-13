import { createHash } from "node:crypto";
import { mkdir, readdir, readFile, rename, rm, writeFile, lstat, mkdtemp } from "node:fs/promises";
import { join } from "node:path";
import { ADMIN_ONLY_SKILLS } from "./admin-skills";
import { skillRunnable } from "./sandbox-service";
import { listInstalledSkills, SKILL_ORIGIN_FILE, type InstalledSkill } from "./workspace";

const MAX_SKILL_NAME = 64;
const SKILL_NAME = /^[a-zA-Z0-9._-]+$/;

export function validSkillName(name: string): boolean {
  return name !== "." && name !== ".." && SKILL_NAME.test(name);
}

/** Same as memberFolderName() in apps/openneko/internal/webfront/memberskills.go. */
export function memberFolderName(userId: string): string {
  const slug = userId.replace(/[^a-zA-Z0-9_-]/g, "-").replace(/^-+|-+$/g, "");
  const safe = slug === userId ? slug : `${slug}-${createHash("sha256").update(userId).digest("hex").slice(0, 8)}`;
  return safe || "anon";
}

export function memberSkillsRoot(orgRoot: string, userId: string): string {
  return join(orgRoot, "members", memberFolderName(userId), "skills");
}

async function hasFiles(dir: string): Promise<boolean> {
  for (const entry of await readdir(dir, { withFileTypes: true }).catch(() => [])) {
    if (entry.isFile()) return true;
    if (entry.isDirectory() && (await hasFiles(join(dir, entry.name)))) return true;
  }
  return false;
}

/** Same as ownRunSkills() in memberskills.go: each own skill folder with files, described from SKILL.md when it can be. */
export async function ownRunSkills(orgRoot: string, userId: string | null | undefined): Promise<InstalledSkill[]> {
  if (!userId) return [];
  const root = memberSkillsRoot(orgRoot, userId);
  const described = new Map((await listInstalledSkills(root)).map((skill) => [skill.name, skill.description]));
  const out: InstalledSkill[] = [];
  for (const entry of await readdir(root, { withFileTypes: true }).catch(() => [])) {
    if (entry.isDirectory() && validSkillName(entry.name) && (await hasFiles(join(root, entry.name)))) {
      out.push({ name: entry.name, description: described.get(entry.name) ?? "" });
    }
  }
  return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

/**
 * Adds the member's own skills to a run's skills and scope. An own skill
 * takes the place of a team skill of the same name. An undefined scope
 * stays undefined: the run may load every skill.
 */
export function runSkillsWithOwn(
  team: readonly InstalledSkill[],
  allowed: readonly string[] | undefined,
  own: readonly InstalledSkill[],
  admin: boolean,
  terminal: boolean,
): { skills: InstalledSkill[]; allowed: string[] | undefined; ownNames: string[] } {
  const ownNames = own
    .map((skill) => skill.name)
    .filter((name) => (admin || !ADMIN_ONLY_SKILLS.has(name)) && skillRunnable(name, terminal));
  const skills = [
    ...team.filter((skill) => !ownNames.includes(skill.name)),
    ...own.filter((skill) => ownNames.includes(skill.name)),
  ].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  if (!allowed) return { skills, allowed: undefined, ownNames };
  return { skills, allowed: [...allowed, ...ownNames.filter((name) => !allowed.includes(name))], ownNames };
}

const UNSAFE = /[^a-z0-9]+/g;
const EDGES = /^-+|-+$/g;

/** The part of a private copy's name that names the member. */
export function memberHandle(email: string | null | undefined, userId: string): string {
  const local = (email ?? "").toLowerCase().split("@")[0] ?? "";
  let handle = local.replace(UNSAFE, "-").replace(EDGES, "");
  if (!handle) handle = userId.toLowerCase().replace(UNSAFE, "-").replace(EDGES, "");
  if (handle.length > 16) handle = handle.slice(0, 16).replace(/-+$/, "");
  return handle || "member";
}

/** <skill>-<handle>, then -2, -3 and on while the name is in use. */
export function privateCopyName(skill: string, handle: string, taken: (name: string) => boolean): string {
  const base = skill.toLowerCase().replace(UNSAFE, "-").replace(EDGES, "");
  for (let n = 1; ; n++) {
    const tail = `-${handle}${n > 1 ? `-${n}` : ""}`;
    let head = base;
    if (head.length + tail.length > MAX_SKILL_NAME) head = head.slice(0, MAX_SKILL_NAME - tail.length).replace(/-+$/, "");
    const name = `${head}${tail}`.replace(/^-+/, "");
    if (!taken(name)) return name;
  }
}

const STOP_WORDS = new Set(["the", "and", "for", "you", "with", "from", "that", "this"]);

/** The member's own skills for a skill search; the org skill index does not hold them. */
export function matchOwnSkills(own: readonly InstalledSkill[], query: string): InstalledSkill[] {
  const words: string[] = [];
  for (const word of query.toLowerCase().split(/[^a-z0-9]+/)) {
    if (word.length >= 3 && !STOP_WORDS.has(word) && !words.includes(word)) words.push(word);
  }
  return own
    .map((skill) => {
      const text = `${skill.name} ${skill.description}`.toLowerCase();
      return { skill, hits: words.filter((word) => text.includes(word)).length };
    })
    .filter((found) => found.hits > 0)
    .sort((a, b) => b.hits - a.hits)
    .map((found) => found.skill);
}

function ignored(part: string): boolean {
  return part === "__pycache__" || part === ".DS_Store" || part.startsWith("._") || part.endsWith(".pyc");
}

/**
 * Copies the regular files of a skill. Links, the built-in origin mark and
 * cache files stay behind. With a name, SKILL.md takes it.
 */
export async function copySkillFolder(src: string, dst: string, name?: string): Promise<void> {
  const walk = async (from: string, to: string, top: boolean): Promise<void> => {
    await mkdir(to, { recursive: true });
    for (const entry of await readdir(from, { withFileTypes: true })) {
      if (ignored(entry.name) || (top && entry.name === SKILL_ORIGIN_FILE)) continue;
      if (entry.isDirectory()) await walk(join(from, entry.name), join(to, entry.name), false);
      else if (entry.isFile()) {
        let data = await readFile(join(from, entry.name));
        if (top && name && entry.name === "SKILL.md") data = Buffer.from(renameSkillMarkdown(data.toString("utf8"), name));
        await writeFile(join(to, entry.name), data);
      }
    }
  };
  await walk(src, dst, true);
}

function renameSkillMarkdown(text: string, name: string): string {
  const match = /^---\n([\s\S]*?)\n---/.exec(text);
  if (!match) return text;
  const start = "---\n".length;
  const front = match[1].replace(/^name:[^\r\n]*/gm, `name: ${name}`);
  return text.slice(0, start) + front + text.slice(start + match[1].length);
}

/** Writes the copy through a temporary folder, so a failed copy leaves nothing. */
export async function copySkillTo(src: string, ownRoot: string, name: string): Promise<void> {
  if (!(await lstat(src)).isDirectory()) throw new Error("staged skill is not a folder");
  await mkdir(ownRoot, { recursive: true });
  const tmp = await mkdtemp(join(ownRoot, ".copy-"));
  try {
    await copySkillFolder(src, tmp, name);
    await rename(tmp, join(ownRoot, name));
  } finally {
    await rm(tmp, { recursive: true, force: true });
  }
}
