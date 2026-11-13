import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import { lstat, mkdir, mkdtemp, readdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, join, posix, resolve } from "node:path";
import { insertConfigChangeRow } from "./config-change";
import {
  copySkillFolder,
  copySkillTo,
  memberSkillsRoot,
  privateCopyName,
  validSkillName,
} from "./member-skills";
import { skillRunnable } from "./sandbox-service";
import { bundledSkillDir } from "./workspace";

/**
 * Skill edits made inside an agent sandbox are made to the sandbox's copy of
 * the skills tree. These helpers find what changed during a turn and save it.
 */

/** Relative path inside the skills root ("<skill>/<file>") to its sha256. */
export type SkillFileHashes = Map<string, string>;

export type SkillEditActor = { userId: string | null; role: string | null };

export type SavedSkillEdits = {
  /** Team skills saved in place. */
  team: string[];
  /** The actor's own skills saved in place. */
  own: string[];
  /** Team skills a member changed, saved as a private copy: team name to copy name. */
  copies: Record<string, string>;
  /** Files another writer changed on the host during the turn; left as they are. */
  skipped: string[];
};

function ignored(part: string): boolean {
  return part === "__pycache__" || part === ".DS_Store" || part.startsWith("._") || part.endsWith(".pyc");
}

function safeSkillPath(path: string): boolean {
  const parts = path.split("/");
  return parts.length >= 2 && parts.every((part) => part !== "" && part !== "." && part !== ".." && !ignored(part));
}

export function skillFileHash(content: Buffer): string {
  return createHash("sha256").update(content).digest("hex");
}

export async function hashSkillTree(skillsRoot: string): Promise<SkillFileHashes> {
  const hashes: SkillFileHashes = new Map();
  const walk = async (directory: string, prefix: string): Promise<void> => {
    const entries = await readdir(directory, { withFileTypes: true }).catch(() => []);
    for (const entry of entries) {
      if (ignored(entry.name)) continue;
      const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) await walk(join(directory, entry.name), relative);
      else if (entry.isFile() && prefix) hashes.set(relative, skillFileHash(await readFile(join(directory, entry.name))));
    }
  };
  await walk(resolve(skillsRoot), "");
  return hashes;
}

/** Python run inside the sandbox: prints the same hash map as hashSkillTree. */
export const SANDBOX_SKILL_HASH_COMMAND = `
import hashlib, json, os, sys
root = sys.argv[1]
skip = lambda p: p == "__pycache__" or p == ".DS_Store" or p.startswith("._") or p.endswith(".pyc")
out = {}
if os.path.isdir(root):
    for directory, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if not skip(d)]
        rel = os.path.relpath(directory, root)
        if rel == ".":
            continue
        for name in files:
            if skip(name):
                continue
            with open(os.path.join(directory, name), "rb") as handle:
                out[(rel + "/" + name).replace(os.sep, "/")] = hashlib.sha256(handle.read()).hexdigest()
print(json.dumps(out))
`;

/**
 * Files the sandbox added or changed, and files it removed from a skill it
 * still holds. A skill the run never received is not treated as removed.
 */
export function diffSkillTrees(baseline: SkillFileHashes, box: SkillFileHashes): { changed: string[]; removed: string[] } {
  const changed = [...box].filter(([path, hash]) => safeSkillPath(path) && baseline.get(path) !== hash).map(([path]) => path);
  const boxSkills = new Set([...box.keys()].map((path) => path.split("/")[0]));
  const removed = [...baseline.keys()].filter((path) => !box.has(path) && boxSkills.has(path.split("/")[0]));
  return { changed: changed.sort(), removed: removed.sort() };
}

function skillOf(path: string): string {
  return path.split("/")[0];
}

/** The run received the skill from the team or the image, or the team folder now holds it. */
function isTeamSkill(teamRoot: string, baseline: SkillFileHashes, name: string): boolean {
  if ([...baseline.keys()].some((path) => skillOf(path) === name)) return true;
  if (existsSync(join(teamRoot, name))) return true;
  const bundled = bundledSkillDir(name);
  return bundled !== "" && existsSync(join(bundled, "SKILL.md"));
}

/**
 * Same as writeFileKeepMode() in apps/openneko/internal/webfront/workspace.go:
 * a reader in another process sees the whole old file or the whole new one,
 * and an edited script keeps its mode.
 */
async function writeFileKeepMode(path: string, content: Buffer): Promise<void> {
  const mode = await stat(path).then((s) => s.mode & 0o777, () => 0o644);
  const tmp = join(dirname(path), `.${basename(path)}.tmp-${process.pid}-${Date.now().toString(36)}`);
  try {
    await writeFile(tmp, content, { mode });
    await rename(tmp, path);
  } finally {
    await rm(tmp, { force: true });
  }
}

function nameTaken(teamRoot: string, ownRoot: string, name: string): boolean {
  return existsSync(join(teamRoot, name)) || existsSync(join(ownRoot, name));
}

/**
 * Same as saveSkillEdits() in apps/openneko/internal/webfront/turnskills.go.
 * An own skill the run staged is saved in place. A team skill is saved in
 * place for an admin or an unattended run; a member's edit becomes a
 * private copy and the team skill stays as it is. A new skill from a member
 * is their own skill.
 */
export async function saveSkillEdits(opts: {
  orgRoot: string;
  orgId: string;
  runId: string;
  actor: SkillEditActor;
  baseline: SkillFileHashes;
  ownSkills?: ReadonlySet<string>;
  memberHandle?: string;
  /** Local folder that holds each changed skill's full folder after the run. */
  skillFolders?: string;
  changed: Array<{ path: string; content: Buffer }>;
  removed: string[];
}): Promise<SavedSkillEdits> {
  const teamRoot = join(resolve(opts.orgRoot), "skills");
  const userId = opts.actor.userId;
  const ownRoot = userId ? memberSkillsRoot(resolve(opts.orgRoot), userId) : "";
  const member = !!userId && opts.actor.role !== "admin";
  const own = opts.ownSkills ?? new Set<string>();
  const names = [...new Set([...opts.changed.map((file) => file.path), ...opts.removed].map(skillOf))].sort();
  const team: string[] = [];
  const mine: string[] = [];
  const copies: string[] = [];
  for (const name of names) {
    if (own.has(name) && userId) mine.push(name);
    else if (!member) team.push(name);
    else if (isTeamSkill(teamRoot, opts.baseline, name)) copies.push(name);
    else if (validSkillName(name) && !nameTaken(teamRoot, ownRoot, name)) mine.push(name);
    else copies.push(name);
  }
  const out: SavedSkillEdits = { team: [], own: [], copies: {}, skipped: [] };

  const save = async (root: string, skills: string[]) => {
    const current = await hashSkillTree(root);
    const unchanged = (path: string) => current.get(path) === opts.baseline.get(path);
    for (const file of opts.changed) {
      if (!skills.includes(skillOf(file.path))) continue;
      if (!unchanged(file.path)) {
        out.skipped.push(file.path);
        continue;
      }
      const target = join(root, file.path);
      await mkdir(dirname(target), { recursive: true });
      await writeFileKeepMode(target, file.content);
    }
    for (const path of opts.removed) {
      if (!skills.includes(skillOf(path))) continue;
      if (!unchanged(path)) {
        out.skipped.push(path);
        continue;
      }
      await rm(join(root, path), { force: true });
    }
  };
  const message = (skills: string[]) =>
    `Updated ${skills.length === 1 ? "skill" : "skills"} in run ${opts.runId.slice(0, 8)}: ${skills.join(", ")}`;

  if (team.length > 0) {
    await save(teamRoot, team);
    await insertConfigChangeRow({
      orgId: opts.orgId,
      artifactKind: "skill",
      artifactRef: team.join(", "),
      actorUserId: userId,
      summary: message(team),
    });
    out.team = team;
  }
  if (mine.length > 0 && userId) {
    await save(ownRoot, mine);
    await insertConfigChangeRow({
      orgId: opts.orgId,
      artifactKind: "skill",
      artifactRef: mine.join(", "),
      actorUserId: userId,
      summary: message(mine),
      scope: "user",
      userId,
    });
    out.own = mine;
  }
  if (copies.length > 0 && userId && opts.skillFolders) {
    for (const skill of copies) {
      const name = privateCopyName(skill, opts.memberHandle ?? "member", (candidate) => nameTaken(teamRoot, ownRoot, candidate));
      try {
        await copySkillTo(join(opts.skillFolders, skill), ownRoot, name);
      } catch (e) {
        console.warn(`[skill-capture] private skill copy failed for ${opts.runId}: ${(e as Error).message}`);
        continue;
      }
      out.copies[skill] = name;
      await insertConfigChangeRow({
        orgId: opts.orgId,
        artifactKind: "skill",
        artifactRef: name,
        actorUserId: userId,
        summary: `Saved the changes to skill ${skill} in run ${opts.runId.slice(0, 8)} as ${name}`,
        scope: "user",
        userId,
      });
    }
  }
  out.skipped.sort();
  return out;
}

/**
 * Same as stageOwnSkills() in apps/openneko/internal/webfront/turnstage.go:
 * each own skill in the run's scope goes over the staged skill of the same
 * name, and its files take the place of the team skill's in the baseline.
 */
export async function stageOwnSkills(opts: {
  ownRoot: string;
  skillsRoot: string;
  own: readonly string[];
  allowed?: readonly string[];
  terminal: boolean;
  baseline: SkillFileHashes;
}): Promise<Set<string>> {
  const staged = new Set<string>();
  const ownFiles = new Set([...(await hashSkillTree(opts.ownRoot)).keys()].map(skillOf));
  for (const name of opts.own) {
    const src = join(opts.ownRoot, name);
    if (!validSkillName(name) || (opts.allowed && !opts.allowed.includes(name)) || !skillRunnable(name, opts.terminal)) continue;
    if (!ownFiles.has(name) || !(await lstat(src).then((info) => info.isDirectory()).catch(() => false))) continue;
    const dest = join(opts.skillsRoot, name);
    await rm(dest, { recursive: true, force: true });
    await copySkillFolder(src, dest);
    staged.add(name);
  }
  if (staged.size === 0) return staged;
  for (const path of [...opts.baseline.keys()]) if (staged.has(skillOf(path))) opts.baseline.delete(path);
  for (const [path, hash] of await hashSkillTree(opts.skillsRoot)) if (staged.has(skillOf(path))) opts.baseline.set(path, hash);
  return staged;
}

export type SkillToolCall = { name: string; input?: unknown };

export type SkillCaptureDecision = { capture: boolean; reason: string };

const SHELL_TOOLS = new Set(["terminal", "process", "execute_code", "execute", "delegate_task"]);
const WRITE_TOOLS = new Set(["file_write", "file_edit", "write_file", "patch", "edit", "delete", "move"]);
const SKILL_TOOL = /(^|[^a-z0-9])(create_skill|skill_manage)$/;

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function toolPaths(input: unknown): string[] {
  const top = record(input);
  const raw = record(top?.rawInput);
  const locations = Array.isArray(top?.locations) ? top.locations : [];
  return [top?.path, raw?.path, ...locations.map((location) => record(location)?.path)]
    .filter((path): path is string => typeof path === "string" && path.length > 0);
}

function touchesSkills(path: string, cwd: string, skillsRoots: readonly string[]): boolean {
  if (path.startsWith("~")) return true;
  const target = posix.resolve(cwd, path);
  if (target.split("/").includes("skills")) return true;
  return skillsRoots.some((root) => {
    const relative = posix.relative(root, target);
    return relative === "" || (!relative.startsWith("..") && !posix.isAbsolute(relative));
  });
}

/**
 * Decides from the run's tool calls whether the sandbox skills tree can have
 * changed. A shell can write anywhere, and missing evidence means capture.
 */
export function skillCaptureDecision(evidence: {
  toolCalls: readonly SkillToolCall[];
  cwd: string;
  skillsRoots: readonly string[];
} | null): SkillCaptureDecision {
  if (!evidence) return { capture: true, reason: "evidence_missing" };
  for (const call of evidence.toolCalls) {
    if (SKILL_TOOL.test(call.name)) return { capture: true, reason: "skill_tool" };
    if (SHELL_TOOLS.has(call.name)) return { capture: true, reason: "shell" };
    if (!WRITE_TOOLS.has(call.name)) continue;
    const title = record(call.input)?.title;
    if (typeof title === "string" && title.startsWith("skill ")) return { capture: true, reason: "skill_tool" };
    const paths = toolPaths(call.input);
    if (paths.length === 0) return { capture: true, reason: "write_path_unknown" };
    if (paths.some((path) => touchesSkills(path, evidence.cwd, evidence.skillsRoots))) {
      return { capture: true, reason: "skill_write" };
    }
  }
  return { capture: false, reason: "no_skill_writes" };
}

/**
 * Compare the sandbox's skills tree with the tree the run received, fetch the
 * skills that changed, and save them. Returns null when nothing changed.
 */
export async function captureSandboxSkillEdits(opts: {
  /** Runs SANDBOX_SKILL_HASH_COMMAND with the sandbox skills root and returns stdout. */
  listSandboxHashes: () => Promise<string>;
  /** Copies one sandbox skill directory into a local directory. */
  downloadSkill: (skill: string, destination: string) => Promise<void>;
  orgRoot: string;
  orgId: string;
  runId: string;
  actor: SkillEditActor;
  baseline: SkillFileHashes;
  ownSkills?: ReadonlySet<string>;
  memberHandle?: string;
}): Promise<SavedSkillEdits | null> {
  const listed = JSON.parse((await opts.listSandboxHashes()).trim().split("\n").pop() || "{}") as Record<string, string>;
  const { changed, removed } = diffSkillTrees(opts.baseline, new Map(Object.entries(listed)));
  if (changed.length === 0 && removed.length === 0) return null;

  const scratch = await mkdtemp(join(tmpdir(), "neko-skill-capture-"));
  try {
    const files: Array<{ path: string; content: Buffer }> = [];
    for (const skill of [...new Set([...changed, ...removed].map(skillOf))]) {
      const destination = join(scratch, skill);
      await mkdir(destination, { recursive: true });
      await opts.downloadSkill(skill, destination);
      for (const path of changed.filter((candidate) => skillOf(candidate) === skill)) {
        const local = join(scratch, path);
        if (existsSync(local)) files.push({ path, content: await readFile(local) });
      }
    }
    return await saveSkillEdits({ ...opts, skillFolders: scratch, changed: files, removed });
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}
