import { cp, lstat, mkdir, readdir, symlink, writeFile } from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { access } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { AgentWorkspace } from "../agent-backend";

const HERE = dirname(fileURLToPath(import.meta.url));
const BUILTIN_SKILLS_ROOT = resolve(HERE, "..", "..", "assets", "builtin-skills");

function getHome(): string {
  return process.env.HOME || homedir();
}

function safeSegment(raw: string): string {
  return raw.replace(/[^a-zA-Z0-9._-]/g, "_");
}

export function getOrgAgentRoot(orgId: string): string {
  return join(
    getHome(),
    ".config",
    "openneko",
    "agents",
    "orgs",
    safeSegment(orgId),
  );
}

export type OrgWorkspaceRoots = Omit<
  AgentWorkspace,
  "threadUploadsRoot" | "runRoot" | "artifactRoot" | "binRoot"
>;

export async function ensureOrgWorkspace(orgId: string): Promise<OrgWorkspaceRoots> {
  const orgRoot = getOrgAgentRoot(orgId);
  const skillsRoot = join(orgRoot, "skills");
  const memoryRoot = join(orgRoot, "memory");
  const knowledgeRoot = join(orgRoot, "knowledge");
  const uploadsRoot = join(orgRoot, "uploads");
  const runsRoot = join(orgRoot, "runs");
  const claudeProjectRoot = orgRoot;
  const claudeConfigRoot = join(orgRoot, "claude", "config");

  for (const dir of [
    orgRoot,
    skillsRoot,
    memoryRoot,
    knowledgeRoot,
    uploadsRoot,
    runsRoot,
    claudeConfigRoot,
  ]) {
    await mkdir(dir, { recursive: true });
  }

  await seedBuiltinSkills(skillsRoot);
  await ensureKnowledgeFiles(knowledgeRoot);
  await ensureLink(join(claudeProjectRoot, ".claude", "skills"), skillsRoot);

  return {
    orgRoot,
    skillsRoot,
    memoryRoot,
    knowledgeRoot,
    uploadsRoot,
    runsRoot,
    claudeProjectRoot,
    claudeConfigRoot,
  };
}

export async function ensureWorkWorkspace(
  orgId: string,
  threadId: string,
  runId: string,
): Promise<AgentWorkspace> {
  const base = await ensureOrgWorkspace(orgId);
  const threadUploadsRoot = join(base.uploadsRoot, safeSegment(threadId));
  const runRoot = join(base.runsRoot, safeSegment(runId));
  const artifactRoot = join(runRoot, "artifacts");
  const binRoot = join(runRoot, "bin");

  for (const dir of [threadUploadsRoot, runRoot, artifactRoot, binRoot]) {
    await mkdir(dir, { recursive: true });
  }

  return {
    ...base,
    threadUploadsRoot,
    runRoot,
    artifactRoot,
    binRoot,
  };
}

export async function listSkillNames(skillsRoot: string): Promise<string[]> {
  try {
    const entries = await readdir(skillsRoot, { withFileTypes: true });
    return entries
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort();
  } catch {
    return [];
  }
}

async function seedBuiltinSkills(skillsRoot: string): Promise<void> {
  const entries = await readdir(BUILTIN_SKILLS_ROOT, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const dest = join(skillsRoot, entry.name);
    try {
      await access(dest, fsConstants.F_OK);
      continue;
    } catch {
      await cp(join(BUILTIN_SKILLS_ROOT, entry.name), dest, {
        recursive: true,
        errorOnExist: false,
      });
    }
  }
}

async function ensureKnowledgeFiles(root: string): Promise<void> {
  const files: Array<[string, string]> = [
    [
      join(root, "INDEX.md"),
      [
        "# Neko Work Knowledge",
        "",
        "This directory is reserved for org-specific durable knowledge files.",
        "Use these paths when you need reusable facts beyond the active chat.",
        "",
        "- schema.json",
        "- insights.json",
        "- syntax.json",
      ].join("\n"),
    ],
    [join(root, "schema.json"), "{}\n"],
    [join(root, "insights.json"), "{}\n"],
    [join(root, "syntax.json"), "{}\n"],
  ];

  for (const [path, content] of files) {
    try {
      await access(path, fsConstants.F_OK);
    } catch {
      await writeFile(path, content, "utf8");
    }
  }
}

async function ensureLink(linkPath: string, target: string): Promise<void> {
  await mkdir(dirname(linkPath), { recursive: true });

  try {
    const stat = await lstat(linkPath);
    if (stat.isSymbolicLink()) return;
    return;
  } catch {
    // create below
  }

  await symlink(target, linkPath, "dir");
}
