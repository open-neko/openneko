import { createHash } from "node:crypto";
import { mkdir, mkdtemp, lstat, rename, rm, writeFile } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { writeWorkSkillFiles, type WorkSkillDraft } from "./skill-files";

const NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** Recheck the persisted Work actor at dispatch, including disable/revocation. */
export async function assertHarnessSkillAuthor(orgId: string, runId: string): Promise<void> {
  const { and, app_user, db, eq, work_run } = await import("@neko/db");
  const [run] = await db().select({userId:work_run.actor_user_id,role:work_run.actor_role})
    .from(work_run).where(and(eq(work_run.id,runId),eq(work_run.org_id,orgId))).limit(1);
  if (!run) throw new Error("Harness skill create requires a current Work actor");
  if (run.userId) {
    const [user] = await db().select({disabledAt:app_user.disabled_at})
      .from(app_user).where(and(eq(app_user.id,run.userId),eq(app_user.org_id,orgId))).limit(1);
    if (!user || user.disabledAt) throw new Error("Harness skill create requires a current enabled user");
    return;
  }
  const [anyUser] = await db().select({id:app_user.id})
    .from(app_user).where(eq(app_user.org_id,orgId)).limit(1);
  if (anyUser || run.role !== "admin") {
    throw new Error("Harness skill create requires a current Work actor");
  }
  // A solo no-user admin run is accepted like the existing rule builder.
}

/** Stage a complete new skill, then publish its directory in one rename.
 * This deliberately cannot replace a skill installed by another run. */
export async function publishHarnessSkill(
  skillsRoot: string,
  draft: WorkSkillDraft,
  operationBinding: string,
): Promise<{ name: string; skillPath: string }> {
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
  const root = resolve(skillsRoot);
  await mkdir(root, { recursive: true });
  const rootStat = await lstat(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) {
    throw new Error("Invalid skill root");
  }
  const target = join(root, draft.name);
  const staging = await mkdtemp(join(root, ".harness-create-"));
  try {
    const { skillPath } = await writeWorkSkillFiles(staging, draft);
    if (skillPath !== join(staging, draft.name) || !skillPath.startsWith(staging + sep)) {
      throw new Error("Invalid staged skill path");
    }
    await writeFile(join(skillPath, ".harness-create"),
      JSON.stringify({ binding: operationBinding,
        digest: createHash("sha256").update(JSON.stringify(draft)).digest("hex") }),
      { flag: "wx", mode: 0o600 });
    try {
      await lstat(target);
      throw new Error("Skill already exists; use a new name");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
    await rename(skillPath, target);
    return { name: draft.name, skillPath: target };
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
}
