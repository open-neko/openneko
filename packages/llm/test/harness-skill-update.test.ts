import { mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { publishHarnessSkill } from "../src/work/harness-skill-create";
import { harnessSkillVersion, recoverHarnessSkillUpdates, replaceHarnessSkill } from "../src/work/harness-skill-update";

const roots:string[]=[];
afterEach(async()=>{await Promise.all(roots.splice(0).map(root=>rm(root,{recursive:true,force:true})));});

it("replaces all files only at the inspected tree version",async()=>{
  const workspace=await mkdtemp(join(tmpdir(),"harness-skill-update-")); roots.push(workspace);
  const skills=join(workspace,"skills"),binding="a".repeat(64);
  await publishHarnessSkill(skills,{name:"review-leads",description:"Review leads",body:"Old instructions",
    files:[{path:"scripts/old.py",content:"print('old')"}]},binding);
  const prior=await harnessSkillVersion(skills,"review-leads");
  const updated=await replaceHarnessSkill(skills,{name:"review-leads",description:"Review leads",body:"New instructions",
    files:[{path:"scripts/new.py",content:"print('new')"}]},prior,"b".repeat(64));
  expect(updated.version).not.toBe(prior);
  expect(await readFile(join(skills,"review-leads","SKILL.md"),"utf8")).toContain("New instructions");
  expect(await readdir(join(skills,"review-leads","scripts"))).toEqual(["new.py"]);
  await expect(replaceHarnessSkill(skills,{name:"review-leads",description:"Review leads",body:"Stale"},prior,
    "c".repeat(64))).rejects.toThrow("changed since inspection");
  expect(await readdir(workspace)).toEqual(["skills"]);
});

it("restores the old tree after interruption between the two renames",async()=>{
  const workspace=await mkdtemp(join(tmpdir(),"harness-skill-update-")); roots.push(workspace);
  const skills=join(workspace,"skills"),name="review-leads";
  await publishHarnessSkill(skills,{name,description:"Review leads",body:"Old instructions"},"a".repeat(64));
  const prior=await harnessSkillVersion(skills,name);
  const tx=await mkdtemp(join(workspace,".harness-skill-update-"));
  await writeFile(join(tx,"manifest.json"),JSON.stringify({name,expectedVersion:prior,binding:"b".repeat(64)}));
  await rename(join(skills,name),join(tx,"old"));
  await recoverHarnessSkillUpdates(skills,name);
  expect(await harnessSkillVersion(skills,name)).toBe(prior);
  expect(await readdir(workspace)).toEqual(["skills"]);
});

it("keeps the published tree after interruption before backup cleanup",async()=>{
  const workspace=await mkdtemp(join(tmpdir(),"harness-skill-update-")); roots.push(workspace);
  const skills=join(workspace,"skills"),name="review-leads";
  await publishHarnessSkill(skills,{name,description:"Review leads",body:"Old instructions"},"a".repeat(64));
  const prior=await harnessSkillVersion(skills,name);
  const tx=await mkdtemp(join(workspace,".harness-skill-update-"));
  const binding="b".repeat(64);
  await writeFile(join(tx,"manifest.json"),JSON.stringify({name,expectedVersion:prior,binding}));
  await rename(join(skills,name),join(tx,"old"));
  await publishHarnessSkill(skills,{name,description:"Review leads",body:"New instructions"},binding);
  const updated=await harnessSkillVersion(skills,name);
  await recoverHarnessSkillUpdates(skills,name);
  expect(await harnessSkillVersion(skills,name)).toBe(updated);
  expect(await readFile(join(skills,name,"SKILL.md"),"utf8")).toContain("New instructions");
  expect(await readdir(workspace)).toEqual(["skills"]);
});
