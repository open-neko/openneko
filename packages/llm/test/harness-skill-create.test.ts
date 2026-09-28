import { mkdtemp, readFile, readdir, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { publishHarnessSkill } from "../src/work/harness-skill-create";

const roots: string[] = [];
afterEach(async () => {
  await Promise.all(roots.splice(0).map(root => rm(root, {recursive:true,force:true})));
});

it("publishes one complete new skill and rejects replacement", async () => {
  const root=await mkdtemp(join(tmpdir(),"harness-skill-")); roots.push(root);
  const draft={name:"daily-summary",description:"Summarize one day",body:"Read the staged input and summarize it.",
    files:[{path:"scripts/check.py",content:"print('ok')\n"}]};
  const binding="a".repeat(64);
  const saved=await publishHarnessSkill(root,draft,binding);
  expect(saved.name).toBe("daily-summary");
  expect(await readFile(join(saved.skillPath,"SKILL.md"),"utf8")).toContain("Summarize one day");
  expect(await readFile(join(saved.skillPath,"scripts/check.py"),"utf8")).toBe("print('ok')\n");
  expect(JSON.parse(await readFile(join(saved.skillPath,".harness-create"),"utf8")).binding).toBe(binding);
  await expect(publishHarnessSkill(root,{...draft,body:"replace"},binding)).rejects.toThrow("already exists");
  expect(await readFile(join(saved.skillPath,"SKILL.md"),"utf8")).toContain("Read the staged input");
  expect(await readdir(root)).toEqual(["daily-summary"]);
});

it("rejects traversal and reserved file paths before publication", async () => {
  const root=await mkdtemp(join(tmpdir(),"harness-skill-")); roots.push(root);
  for(const path of ["../outside","/absolute","SKILL.md",".harness-create","scripts/../../outside"]){
    await expect(publishHarnessSkill(root,{name:"safe-skill",description:"safe",body:"safe",
      files:[{path,content:"bad"}]},"b".repeat(64))).rejects.toThrow();
  }
  expect(await readdir(root)).toEqual([]);
});

it("refuses a symlinked skill root",async()=>{
  const root=await mkdtemp(join(tmpdir(),"harness-skill-")); roots.push(root);
  await symlink(root,join(root,"linked"),"dir");
  await expect(publishHarnessSkill(join(root,"linked"),{name:"safe-skill",description:"safe",body:"safe"},
    "c".repeat(64))).rejects.toThrow("Invalid skill root");
  expect(await readdir(root)).toEqual(["linked"]);
});
