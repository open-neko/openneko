import {
  access,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  SKILL_ORIGIN_FILE,
  copySkillOverrides,
  ensureIsolatedJobWorkspace,
  ensureOrgWorkspace,
  ensureWorkWorkspace,
  fingerprintSkillTree,
  materializeBuiltinSkills,
  readSkillOrigin,
  resolveBuiltinSkillsRoot,
  resolveSandboxSkillsRoot,
  unmodifiedBuiltinSkills,
} from "../src/work/workspace";

const cleanupPaths: string[] = [];
const ORIGINAL_NODE_ENV = process.env.NODE_ENV;

beforeEach(() => {
  process.env.OPENNEKO_SANDBOX_URL = "unix:///run/openneko/sandbox.sock";
});

afterEach(async () => {
  delete process.env.OPENNEKO_SANDBOX_URL;
  while (cleanupPaths.length > 0) {
    const path = cleanupPaths.pop();
    if (path) await rm(path, { recursive: true, force: true });
  }
  delete process.env.HOME;
  delete process.env.OPENNEKO_AGENT_HOME;
  delete process.env.OPENNEKO_HOST_WEB_DEV;
  delete process.env.OPENNEKO_STACK_MODE;
  delete process.env.NEXT_PUBLIC_DEMO;
  delete process.env.DEMO;
  if (ORIGINAL_NODE_ENV === undefined) delete process.env.NODE_ENV;
  else process.env.NODE_ENV = ORIGINAL_NODE_ENV;
});

describe("work workspace", () => {
  it("self-locates built-in skills beside the standalone agent bundle", () => {
    expect(
      resolveBuiltinSkillsRoot(
        "file:///app/agent-entry.js",
        undefined,
        (candidate) => candidate === "/app/assets/builtin-skills",
      ),
    ).toBe("/app/assets/builtin-skills");
  });

  it("finds the sandbox skills beside the agent bundle, in the package assets, then in the recipe", () => {
    const all = () => true;
    expect(resolveSandboxSkillsRoot("file:///app/agent-entry.js", undefined, all)).toBe("/app/assets/sandbox-skills");
    expect(
      resolveSandboxSkillsRoot("file:///app/packages/llm/src/work/workspace.ts", undefined, (candidate) => candidate === "/app/packages/llm/assets/sandbox-skills"),
    ).toBe("/app/packages/llm/assets/sandbox-skills");
    expect(
      resolveSandboxSkillsRoot("file:///repo/packages/llm/src/work/workspace.ts", undefined, (candidate) => candidate === "/repo/recipes/sandbox/skills"),
    ).toBe("/repo/recipes/sandbox/skills");
    expect(resolveSandboxSkillsRoot("file:///x/y.js", "/opt/skills", () => false)).toBe("/opt/skills");
  });

  it.each(["xlsx", "docx", "pptx"])("seeds the %s skill from the sandbox recipe without its packages or tests", async (name) => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    const recipe = join(__dirname, "..", "..", "..", "recipes", "sandbox", "skills", name);
    const seeded = join(workspace.skillsRoot, name);
    expect(await readFile(join(seeded, "SKILL.md"), "utf8")).toBe(await readFile(join(recipe, "SKILL.md"), "utf8"));
    expect((await readdir(join(seeded, "scripts"))).sort()).toEqual(["lib", `${name}.cjs`].sort());
    expect(await readdir(seeded)).not.toContain("node_modules");
    expect(await readdir(workspace.skillsRoot)).not.toContain("node_modules");
    expect(await readdir(workspace.skillsRoot)).not.toContain("test");
    expect(await unmodifiedBuiltinSkills(workspace.skillsRoot)).toContain(name);
  }, 30_000);

  it("supports an isolated agent home in explicit host-web development mode", async () => {
    const agentHome = await mkdtemp(join(tmpdir(), "neko-agent-home-"));
    cleanupPaths.push(agentHome);
    process.env.OPENNEKO_AGENT_HOME = agentHome;
    process.env.OPENNEKO_HOST_WEB_DEV = "1";
    process.env.NODE_ENV = "development";

    const workspace = await ensureOrgWorkspace("org-test");
    expect(workspace.orgRoot).toBe(
      join(agentHome, ".config", "openneko", "agents", "orgs", "org-test"),
    );
  }, 30_000);

  it.each([
    ["production", { NODE_ENV: "production" }],
    ["demo stack", { NODE_ENV: "development", OPENNEKO_STACK_MODE: "demo" }],
    ["demo UI", { NODE_ENV: "development", NEXT_PUBLIC_DEMO: "true" }],
  ])("ignores the host-web agent home in %s mode", async (_label, mode) => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    const agentHome = await mkdtemp(join(tmpdir(), "neko-agent-home-"));
    cleanupPaths.push(home, agentHome);
    process.env.HOME = home;
    process.env.OPENNEKO_AGENT_HOME = agentHome;
    process.env.OPENNEKO_HOST_WEB_DEV = "1";
    Object.assign(process.env, mode);

    const workspace = await ensureOrgWorkspace("org-test");
    expect(workspace.orgRoot).toBe(
      join(home, ".config", "openneko", "agents", "orgs", "org-test"),
    );
  }, 30_000);

  it("seeds built-in skills into the org workspace", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    const skills = await readdir(workspace.skillsRoot);
    expect(skills).not.toContain("records");
    expect(skills).toContain("skill-creator");
    expect(skills).toContain("document-extraction");
  }, 30_000);

  it("creates per-thread and per-run directories", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const workspace = await ensureWorkWorkspace("org-test", "thread-1", "run-1");
    expect(workspace.threadUploadsRoot).toContain("thread-1");
    expect(workspace.artifactRoot).toContain("run-1");
  }, 30_000);

  it("stages only custom and modified skill bodies, then fills built-ins from the image", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    const stage = await mkdtemp(join(tmpdir(), "neko-skills-stage-"));
    cleanupPaths.push(home, stage);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    const modified = join(workspace.skillsRoot, "skill-creator", "SKILL.md");
    await writeFile(modified, `${await readFile(modified, "utf8")}\nOrg override.\n`);
    const custom = join(workspace.skillsRoot, "quarterly-review");
    await mkdir(custom, { recursive: true });
    await writeFile(
      join(custom, "SKILL.md"),
      "---\nname: quarterly-review\ndescription: Prepare quarterly reviews\n---\n",
    );

    const stagedSkills = join(stage, "skills");
    const copied = await copySkillOverrides(workspace.skillsRoot, stagedSkills);
    expect(copied).toContain("quarterly-review");
    expect(copied).toContain("skill-creator");
    expect(copied).not.toContain("document-extraction");

    await materializeBuiltinSkills(stagedSkills);
    expect(await readdir(stagedSkills)).toContain("document-extraction");
    expect(await readFile(join(stagedSkills, "skill-creator", "SKILL.md"), "utf8"))
      .toContain("Org override.");
  }, 30_000);

  it("lists seeded built-in skills until someone edits one", async () => {
    const skills = await mkdtemp(join(tmpdir(), "neko-builtin-skills-"));
    cleanupPaths.push(skills);
    await materializeBuiltinSkills(skills);
    const seeded = await unmodifiedBuiltinSkills(skills);
    expect(seeded).toContain("document-extraction");
    expect(seeded).toContain("skill-creator");

    await writeFile(join(skills, "document-extraction", "SKILL.md"), "Edited by an admin.");
    await mkdir(join(skills, "quarterly-review"), { recursive: true });
    await writeFile(join(skills, "quarterly-review", "SKILL.md"), "Org skill.");
    const after = await unmodifiedBuiltinSkills(skills);
    expect(after).not.toContain("document-extraction");
    expect(after).not.toContain("quarterly-review");
    expect(after).toContain("skill-creator");
  }, 30_000);

  it("leaves out the script skills when the install has no sandbox", async () => {
    delete process.env.OPENNEKO_SANDBOX_URL;
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    const stage = await mkdtemp(join(tmpdir(), "neko-skills-stage-"));
    cleanupPaths.push(home, stage);
    process.env.HOME = home;
    const workspace = await ensureOrgWorkspace("org-test");
    const seeded = await readdir(workspace.skillsRoot);
    expect(seeded).toContain("graphjin-config");
    for (const name of ["document-extraction", "docx", "pptx", "skill-creator", "xlsx"]) expect(seeded).not.toContain(name);

    await mkdir(join(workspace.skillsRoot, "document-extraction"), { recursive: true });
    await writeFile(join(workspace.skillsRoot, "document-extraction", "SKILL.md"), "Seeded before the sandbox went away.");
    const stagedSkills = join(stage, "skills");
    expect(await copySkillOverrides(workspace.skillsRoot, stagedSkills)).not.toContain("document-extraction");
    await materializeBuiltinSkills(stagedSkills);
    expect(await readdir(stagedSkills)).not.toContain("document-extraction");
  }, 30_000);

  it("stages and seeds only the skills the run's actor holds", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    const stage = await mkdtemp(join(tmpdir(), "neko-skills-stage-"));
    cleanupPaths.push(home, stage);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    for (const name of ["quarterly-review", "sales-playbook"]) {
      await mkdir(join(workspace.skillsRoot, name), { recursive: true });
      await writeFile(join(workspace.skillsRoot, name, "SKILL.md"), `---\nname: ${name}\ndescription: x\n---\n`);
    }

    const stagedSkills = join(stage, "skills");
    const allowed = ["quarterly-review", "document-extraction"];
    expect(await copySkillOverrides(workspace.skillsRoot, stagedSkills, [], allowed)).toEqual(["quarterly-review"]);
    await materializeBuiltinSkills(stagedSkills, allowed);
    expect((await readdir(stagedSkills)).sort()).toEqual(["document-extraction", "quarterly-review"]);

    const forced = join(stage, "forced");
    expect(await copySkillOverrides(workspace.skillsRoot, forced, ["document-extraction"], [])).toEqual(["document-extraction"]);
  }, 30_000);

  it("records a builtin seed origin and skips a stale unmodified seed", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    const stage = await mkdtemp(join(tmpdir(), "neko-skills-stage-"));
    cleanupPaths.push(home, stage);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    const seedDir = join(workspace.skillsRoot, "document-extraction");
    const origin = await readSkillOrigin(seedDir);
    expect(origin?.kind).toBe("builtin");
    expect(origin?.sourceHash).toBe(await fingerprintSkillTree(seedDir));

    await writeFile(
      join(seedDir, "SKILL.md"),
      `${await readFile(join(seedDir, "SKILL.md"), "utf8")}\nStale seed body.\n`,
    );
    await writeFile(
      join(seedDir, SKILL_ORIGIN_FILE),
      `${JSON.stringify({
        kind: "builtin",
        sourceHash: await fingerprintSkillTree(seedDir),
      })}\n`,
    );

    const stagedSkills = join(stage, "skills");
    const copied = await copySkillOverrides(workspace.skillsRoot, stagedSkills);
    expect(copied).not.toContain("document-extraction");

    await materializeBuiltinSkills(stagedSkills);
    expect(await readFile(join(stagedSkills, "document-extraction", "SKILL.md"), "utf8")).not.toContain(
      "Stale seed body.",
    );
  }, 30_000);

  it("removes unchanged copies of retired built-ins and keeps changed or unrecorded ones", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;
    const workspace = await ensureOrgWorkspace("org-test");
    const seed = async (name: string, body: string, origin: boolean) => {
      const dir = join(workspace.skillsRoot, name);
      await rm(dir, { recursive: true, force: true });
      await mkdir(join(dir, "scripts"), { recursive: true });
      await writeFile(join(dir, "SKILL.md"), `---\nname: ${name}\ndescription: old\n---\n`);
      await writeFile(join(dir, "scripts", "run.py"), body);
      if (origin) {
        const sourceHash = await fingerprintSkillTree(dir);
        await writeFile(join(dir, SKILL_ORIGIN_FILE), `${JSON.stringify({ kind: "builtin", sourceHash })}\n`);
      }
    };
    await seed("xlsx", "print(1)\n", true);
    await seed("dcf-model", "print(1)\n", true);
    await seed("docx", "print(1)\n", true);
    await writeFile(join(workspace.skillsRoot, "docx", "SKILL.md"), "Edited by an admin.");
    await seed("pptx", "print(1)\n", false);
    await seed("quarterly-review", "print(1)\n", true);

    await ensureOrgWorkspace("org-test");
    const after = await readdir(workspace.skillsRoot);
    expect(await readFile(join(workspace.skillsRoot, "xlsx", "SKILL.md"), "utf8")).toContain("# Excel workbooks");
    expect(await readdir(join(workspace.skillsRoot, "xlsx", "scripts"))).not.toContain("run.py");
    expect(after).not.toContain("dcf-model");
    expect(await readFile(join(workspace.skillsRoot, "docx", "SKILL.md"), "utf8")).toBe("Edited by an admin.");
    expect(await readdir(join(workspace.skillsRoot, "pptx", "scripts"))).toEqual(["run.py"]);
    expect(after).toContain("quarterly-review");
    expect(after).toContain("skill-creator");
  }, 30_000);

  it("creates an empty disposable tree for non-interactive agent jobs", async () => {
    const isolated = await ensureIsolatedJobWorkspace("profile/job-1");
    const rootEntries = await readdir(isolated.workspace.orgRoot);

    expect(isolated.workspace.orgRoot).toContain("openneko-agent-profile_job-1-");
    expect(rootEntries.sort()).toEqual([
      "knowledge",
      "memory",
      "runs",
      "skills",
      "uploads",
    ]);
    expect(await readdir(isolated.workspace.memoryRoot)).toEqual([]);
    expect(await readdir(isolated.workspace.skillsRoot)).toEqual([]);

    await isolated.cleanup();
    await expect(access(isolated.workspace.orgRoot)).rejects.toThrow();
  });
});
