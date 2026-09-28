import { lstat, mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { syncHarnessPath, syncHarnessSkillTree, validateHarnessSkillDraft } from "./harness-skill-create";
import { writeWorkSkillFiles, type WorkSkillDraft } from "./skill-files";
import { fingerprintSkillTree } from "./workspace";

const TX_PREFIX = ".harness-skill-update-";
const HEX = /^[a-f0-9]{64}$/;
const NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

type Transaction = { name: string; expectedVersion: string; binding: string };

/** A database advisory lock spans the filesystem effect across worker processes. */
export async function withHarnessSkillLock<T>(orgId:string,name:string,work:()=>Promise<T>):Promise<T> {
  skillPath("/",name); // validate before forming the lock identity
  const { pool } = await import("@neko/db");
  const client=await pool().connect();
  try {
    await client.query("begin");
    await client.query("select pg_advisory_xact_lock(hashtextextended($1::text,0))",[`harness-skill:${orgId}:${name}`]);
    const result=await work();
    await client.query("commit");
    return result;
  } catch(error) {
    await client.query("rollback").catch(()=>undefined);
    throw error;
  } finally { client.release(); }
}

async function exists(path: string): Promise<boolean> {
  try { await lstat(path); return true; }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw error;
  }
}

function skillPath(skillsRoot: string, name: string): string {
  if (!NAME.test(name) || name.length > 64) throw new Error("Invalid skill name");
  return join(resolve(skillsRoot), name);
}

export async function harnessSkillVersion(skillsRoot: string, name: string): Promise<string> {
  const target = skillPath(skillsRoot, name);
  const stat = await lstat(target);
  if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error("Skill is not a regular directory");
  return fingerprintSkillTree(target);
}

/** Restore an interrupted directory swap before reading or changing the skill.
 * Callers must hold the same org/name lock used for publication. */
export async function recoverHarnessSkillUpdates(skillsRoot: string, name: string): Promise<void> {
  const root = resolve(skillsRoot);
  const target = skillPath(root, name);
  const parent = dirname(root);
  const entries = await readdir(parent, { withFileTypes: true });
  for (const entry of entries.sort((a,b)=>a.name.localeCompare(b.name))) {
    if (!entry.isDirectory() || !entry.name.startsWith(TX_PREFIX)) continue;
    const txDir=join(parent,entry.name);
    let manifest:Transaction;
    try { manifest=JSON.parse(await readFile(join(txDir,"manifest.json"),"utf8")) as Transaction; }
    catch { throw new Error("Unverifiable skill update transaction"); }
    if (!manifest || !NAME.test(manifest.name) || !HEX.test(manifest.expectedVersion) || !HEX.test(manifest.binding)) {
      throw new Error("Invalid skill update transaction");
    }
    if (manifest.name !== name) continue;
    const backup=join(txDir,"old");
    const hasBackup=await exists(backup), hasTarget=await exists(target);
    if (hasBackup && !hasTarget) {
      await rename(backup,target); // crash after moving old, before publishing new
      await syncHarnessPath(root);
      await rm(txDir,{recursive:true,force:true});
      await syncHarnessPath(parent);
      continue;
    }
    if (hasBackup && hasTarget) {
      let marker:{binding?:string};
      try { marker=JSON.parse(await readFile(join(target,".harness-create"),"utf8")) as {binding?:string}; }
      catch { throw new Error("Skill update outcome cannot be verified"); }
      if (marker.binding !== manifest.binding) throw new Error("Skill update outcome cannot be verified");
      await rm(txDir,{recursive:true,force:true}); // new tree is published
      await syncHarnessPath(parent);
      continue;
    }
    if (!hasBackup && hasTarget) {
      await rm(txDir,{recursive:true,force:true}); // swap never began
      await syncHarnessPath(parent);
      continue;
    }
    throw new Error("Skill update lost both old and new trees");
  }
}

/** Run before staging a Work turn so a previous process crash cannot hide a
 * skill between the two renames. Each transaction is recovered under its lock. */
export async function recoverPendingHarnessSkillUpdates(orgId:string,skillsRoot:string):Promise<void> {
  const parent=dirname(resolve(skillsRoot));
  let entries;
  try { entries=await readdir(parent,{withFileTypes:true}); }
  catch(error) {
    if ((error as NodeJS.ErrnoException).code==="ENOENT") return;
    throw error;
  }
  const names=new Set<string>();
  for(const entry of entries) {
    if (!entry.isDirectory() || !entry.name.startsWith(TX_PREFIX)) continue;
    let manifest:Transaction;
    try { manifest=JSON.parse(await readFile(join(parent,entry.name,"manifest.json"),"utf8")) as Transaction; }
    catch { throw new Error("Unverifiable skill update transaction"); }
    if (!manifest || !NAME.test(manifest.name)) throw new Error("Invalid skill update transaction");
    names.add(manifest.name);
  }
  for(const name of [...names].sort()) {
    await withHarnessSkillLock(orgId,name,()=>recoverHarnessSkillUpdates(skillsRoot,name));
  }
}

/** Replace the entire skill under a tree-wide optimistic version. A crash can
 * leave a two-rename transaction; recoverHarnessSkillUpdates resolves it. */
export async function replaceHarnessSkill(
  skillsRoot:string, draft:WorkSkillDraft, expectedVersion:string, binding:string,
):Promise<{name:string;version:string}> {
  validateHarnessSkillDraft(draft,binding);
  if (!HEX.test(expectedVersion)) throw new Error("Invalid skill version");
  const root=resolve(skillsRoot), target=skillPath(root,draft.name);
  const rootStat=await lstat(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) throw new Error("Invalid skill root");
  await recoverHarnessSkillUpdates(root,draft.name);
  const current=await harnessSkillVersion(root,draft.name);
  if (current!==expectedVersion) throw new Error("Skill changed since inspection");
  await mkdir(dirname(root),{recursive:true});
  const txDir=await mkdtemp(join(dirname(root),TX_PREFIX));
  const backup=join(txDir,"old");
  try {
    await writeFile(join(txDir,"manifest.json"),JSON.stringify({name:draft.name,expectedVersion,binding}));
    const {skillPath:staged}=await writeWorkSkillFiles(txDir,draft);
    await writeFile(join(staged,".harness-create"),JSON.stringify({binding}),{flag:"wx",mode:0o600});
    await syncHarnessSkillTree(staged);
    await syncHarnessPath(join(txDir,"manifest.json"));
    await syncHarnessPath(txDir);
    await syncHarnessPath(dirname(root));
    // Detect an external edit that did not cooperate with the host lock.
    if (await harnessSkillVersion(root,draft.name)!==expectedVersion) throw new Error("Skill changed during update");
    await rename(target,backup);
    await syncHarnessPath(root);
    await syncHarnessPath(txDir);
    if (await fingerprintSkillTree(backup)!==expectedVersion) throw new Error("Skill changed during publication");
    await rename(staged,target);
    await syncHarnessPath(root);
    await syncHarnessPath(txDir);
    const version=await harnessSkillVersion(root,draft.name);
    await rm(txDir,{recursive:true,force:true});
    await syncHarnessPath(dirname(root));
    return {name:draft.name,version};
  } catch(error) {
    if (await exists(backup) && !(await exists(target))) {
      await rename(backup,target);
      await syncHarnessPath(root);
    }
    // If the new tree was published, leave the manifest and backup for
    // recovery to classify; never undo an effect after losing its receipt.
    if (!(await exists(backup))) {
      await rm(txDir,{recursive:true,force:true});
      await syncHarnessPath(dirname(root));
    }
    throw error;
  }
}
