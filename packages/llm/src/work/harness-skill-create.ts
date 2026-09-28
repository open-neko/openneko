import { createHash } from "node:crypto";
import { mkdir, mkdtemp, lstat, open, readdir, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve, sep } from "node:path";
import { writeWorkSkillFiles, type WorkSkillDraft } from "./skill-files";

const NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export async function syncHarnessPath(path:string):Promise<void> {
  const handle=await open(path,"r");
  try {await handle.sync();} finally {await handle.close();}
}

export async function syncHarnessSkillTree(path:string):Promise<void> {
  const stat=await lstat(path);
  if (stat.isSymbolicLink()) throw new Error("Skill staging contains a symlink");
  if (stat.isDirectory()) {
    for(const name of (await readdir(path)).sort()) await syncHarnessSkillTree(join(path,name));
  } else if (!stat.isFile()) throw new Error("Skill staging contains an unsupported file");
  await syncHarnessPath(path);
}

/** Org-shared skill writes require the current administrator, not a role snapshot. */
export async function assertHarnessSkillAuthor(orgId: string, runId: string): Promise<void> {
  const { and, app_user, db, eq, resolveUserGroups, work_run } = await import("@neko/db");
  const [run] = await db().select({userId:work_run.actor_user_id,role:work_run.actor_role})
    .from(work_run).where(and(eq(work_run.id,runId),eq(work_run.org_id,orgId))).limit(1);
  if (!run) throw new Error("Harness skill write requires a current administrator");
  if (run.userId) {
    const [user] = await db().select({disabledAt:app_user.disabled_at})
      .from(app_user).where(and(eq(app_user.id,run.userId),eq(app_user.org_id,orgId))).limit(1);
    if (!user || user.disabledAt || !(await resolveUserGroups(orgId,run.userId)).administrator) {
      throw new Error("Harness skill write requires a current administrator");
    }
    return;
  }
  const [anyUser] = await db().select({id:app_user.id})
    .from(app_user).where(eq(app_user.org_id,orgId)).limit(1);
  if (anyUser || run.role !== "admin") {
    throw new Error("Harness skill write requires a current administrator");
  }
  // A solo no-user admin run is accepted like the existing rule builder.
}

export function validateHarnessSkillDraft(draft: WorkSkillDraft, operationBinding: string): void {
  if (!NAME.test(draft.name) || draft.name.length > 64 ||
      !draft.description.trim() || draft.description.length > 1024 ||
      !draft.body.trim() || draft.body.length > 60000 ||
      !/^[a-f0-9]{64}$/.test(operationBinding) ||
      (draft.files?.length ?? 0) > 10) {
    throw new Error("Invalid skill draft");
  }
  const seen = new Set(["skill.md", ".harness-create"]);
  for (const file of draft.files ?? []) {
    const path = file.path.replaceAll("\\", "/");
    const parts = path.split("/");
    if (!path || path.startsWith("/") || parts.some(part =>
      !part || part === "." || part === ".." || part.startsWith(".")) ||
      file.content.length > 32000 || seen.has(path.toLowerCase())) {
      throw new Error("Invalid skill supporting file");
    }
    seen.add(path.toLowerCase());
  }
  if (Buffer.byteLength(JSON.stringify(draft)) > 120000) {
    throw new Error("Skill draft exceeds size limit");
  }
}

/** Stage a complete new skill, then publish its directory in one rename.
 * This deliberately cannot replace a skill installed by another run. */
export async function publishHarnessSkill(
  skillsRoot: string,
  draft: WorkSkillDraft,
  operationBinding: string,
): Promise<{ name: string; skillPath: string }> {
  validateHarnessSkillDraft(draft,operationBinding);
  const root = resolve(skillsRoot);
  await mkdir(root, { recursive: true });
  const rootStat = await lstat(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) {
    throw new Error("Invalid skill root");
  }
  const target = join(root, draft.name);
  // Keep incomplete drafts outside the catalog watched by other runs.
  const staging = await mkdtemp(join(dirname(root), ".harness-skill-create-"));
  try {
    const { skillPath } = await writeWorkSkillFiles(staging, draft);
    if (skillPath !== join(staging, draft.name) || !skillPath.startsWith(staging + sep)) {
      throw new Error("Invalid staged skill path");
    }
    await writeFile(join(skillPath, ".harness-create"),
      JSON.stringify({ binding: operationBinding,
        digest: createHash("sha256").update(JSON.stringify(draft)).digest("hex") }),
      { flag: "wx", mode: 0o600 });
    await syncHarnessSkillTree(skillPath);
    try {
      await lstat(target);
      throw new Error("Skill already exists; use a new name");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
    await rename(skillPath, target);
    await syncHarnessPath(root);
    return { name: draft.name, skillPath: target };
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
}
