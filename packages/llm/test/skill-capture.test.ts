import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { chmod, cp, mkdir, mkdtemp, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  captureSandboxSkillEdits,
  diffSkillTrees,
  hashSkillTree,
  SANDBOX_SKILL_HASH_COMMAND,
  skillCaptureDecision,
  type SkillToolCall,
} from "../src/work/skill-capture";
import { memberSkillsRoot } from "../src/work/member-skills";

let root: string;
let orgRoot: string;
let box: string;
let boxHashes = new Map<string, string>();

async function put(base: string, path: string, content: string) {
  await mkdir(join(base, path, ".."), { recursive: true });
  await writeFile(join(base, path), content);
}

function capture(
  actor: { userId: string | null; role: string | null },
  baseline: Map<string, string>,
  ownSkills: Set<string> = new Set(),
) {
  return captureSandboxSkillEdits({
    ownSkills,
    memberHandle: "ann",
    listSandboxHashes: async () => `noise\n${JSON.stringify(Object.fromEntries(boxHashes))}`,
    downloadSkill: async (skill, destination) => cp(join(box, skill), destination, { recursive: true }),
    orgRoot,
    orgId: "org-1",
    runId: "run-12345678-aaaa",
    actor,
    baseline,
  });
}


beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), "skill-capture-"));
  orgRoot = join(root, "org-1");
  box = join(root, "box");
  await put(join(orgRoot, "skills"), "shortfall/SKILL.md", "# Shortfall\n");
  await put(join(orgRoot, "skills"), "shortfall/scripts/calculate.py", "print(1)\n");
  await put(join(orgRoot, "skills"), "other/SKILL.md", "# Other\n");
  await chmod(join(orgRoot, "skills/shortfall/scripts/calculate.py"), 0o755);
  await cp(join(orgRoot, "skills"), box, { recursive: true });
  await rm(join(box, "other"), { recursive: true });
});

afterEach(async () => {
  await rm(root, { recursive: true, force: true });
});

describe("SANDBOX_SKILL_HASH_COMMAND", () => {
  it("lists the same hashes as the host", async () => {
    await put(box, "shortfall/__pycache__/calculate.cpython-311.pyc", "x");
    await put(box, "shortfall/._SKILL.md", "x");
    const listed = execFileSync("python3", ["-c", SANDBOX_SKILL_HASH_COMMAND, box], { encoding: "utf8" });
    expect(new Map(Object.entries(JSON.parse(listed)))).toEqual(await hashSkillTree(box));
  });
});

describe("diffSkillTrees", () => {
  it("finds changed, added and removed files, and ignores skills the run never received", () => {
    const baseline = new Map([["a/SKILL.md", "1"], ["a/old.py", "2"], ["b/SKILL.md", "3"]]);
    const sandbox = new Map([["a/SKILL.md", "9"], ["a/new.py", "4"], ["a/__pycache__/x.pyc", "5"]]);
    expect(diffSkillTrees(baseline, sandbox)).toEqual({ changed: ["a/SKILL.md", "a/new.py"], removed: ["a/old.py"] });
  });
});

describe("captureSandboxSkillEdits", () => {
  it("saves an admin's sandbox edits to the company skills in place", async () => {
    const baseline = await hashSkillTree(join(orgRoot, "skills"));
    await put(box, "shortfall/scripts/calculate.py", "print(2)\n");
    await put(box, "shortfall/references/new.md", "notes\n");
    boxHashes = await hashSkillTree(box);

    const saved = await capture({ userId: "u-admin", role: "admin" }, baseline);

    expect(saved).toMatchObject({ team: ["shortfall"], own: [], copies: {}, skipped: [] });
    const script = join(orgRoot, "skills/shortfall/scripts/calculate.py");
    expect(await readFile(script, "utf8")).toBe("print(2)\n");
    expect((await stat(script)).mode & 0o777).toBe(0o755);
    expect(await readdir(join(orgRoot, "skills/shortfall/scripts"))).toEqual(["calculate.py"]);
    expect(await readFile(join(orgRoot, "skills/shortfall/references/new.md"), "utf8")).toBe("notes\n");
    expect(await readFile(join(orgRoot, "skills/other/SKILL.md"), "utf8")).toBe("# Other\n");
    expect(existsSync(join(orgRoot, ".git"))).toBe(false);
  });

  it("keeps a host change made by another writer during the turn", async () => {
    const baseline = await hashSkillTree(join(orgRoot, "skills"));
    await put(join(orgRoot, "skills"), "shortfall/SKILL.md", "# Edited elsewhere\n");
    await put(box, "shortfall/SKILL.md", "# Edited in the box\n");
    boxHashes = await hashSkillTree(box);

    const saved = await capture({ userId: null, role: null }, baseline);

    expect(saved?.skipped).toEqual(["shortfall/SKILL.md"]);
    expect(await readFile(join(orgRoot, "skills/shortfall/SKILL.md"), "utf8")).toBe("# Edited elsewhere\n");
  });

  it("saves a member's edit to a team skill as a private copy and leaves the team skill", async () => {
    const baseline = await hashSkillTree(join(orgRoot, "skills"));
    await put(box, "shortfall/SKILL.md", "---\nname: shortfall\n---\nMine\n");
    await rm(join(box, "shortfall/scripts/calculate.py"));
    boxHashes = await hashSkillTree(box);

    const saved = await capture({ userId: "u-member", role: "member" }, baseline);

    expect(saved).toMatchObject({ team: [], own: [], copies: { shortfall: "shortfall-ann" } });
    expect(await readFile(join(orgRoot, "skills/shortfall/SKILL.md"), "utf8")).toBe("# Shortfall\n");
    const own = memberSkillsRoot(orgRoot, "u-member");
    expect(await readFile(join(own, "shortfall-ann/SKILL.md"), "utf8")).toBe("---\nname: shortfall-ann\n---\nMine\n");
    expect(existsSync(join(own, "shortfall-ann/scripts/calculate.py"))).toBe(false);

    await put(box, "shortfall/SKILL.md", "---\nname: shortfall\n---\nAgain\n");
    boxHashes = await hashSkillTree(box);
    expect((await capture({ userId: "u-member", role: "member" }, baseline))?.copies).toEqual({ shortfall: "shortfall-ann-2" });
  });

  it("saves a member's own skill in place and a new skill as their own", async () => {
    const own = memberSkillsRoot(orgRoot, "u-member");
    await put(own, "notes/SKILL.md", "# Notes\n");
    await put(box, "notes/SKILL.md", "# Notes\n");
    const baseline = await hashSkillTree(join(orgRoot, "skills"));
    baseline.set("notes/SKILL.md", (await hashSkillTree(own)).get("notes/SKILL.md")!);
    await put(box, "notes/SKILL.md", "# Notes, edited\n");
    await put(box, "fresh/SKILL.md", "# Fresh\n");
    boxHashes = await hashSkillTree(box);

    const saved = await capture({ userId: "u-member", role: "member" }, baseline, new Set(["notes"]));

    expect(saved).toMatchObject({ team: [], own: ["fresh", "notes"], copies: {} });
    expect(await readFile(join(own, "notes/SKILL.md"), "utf8")).toBe("# Notes, edited\n");
    expect(await readFile(join(own, "fresh/SKILL.md"), "utf8")).toBe("# Fresh\n");
    expect(existsSync(join(orgRoot, "skills/fresh"))).toBe(false);
  });

  it("does nothing when the sandbox skills match what the run received", async () => {
    const baseline = await hashSkillTree(join(orgRoot, "skills"));
    boxHashes = await hashSkillTree(box);
    expect(await capture({ userId: "u-admin", role: "admin" }, baseline)).toBeNull();
  });
});

describe("skillCaptureDecision", () => {
  const decide = (...toolCalls: SkillToolCall[]) =>
    skillCaptureDecision({ toolCalls, cwd: "/sandbox/org-1", skillsRoots: ["/sandbox/org-1/skills"] });

  it("skips a run whose tools wrote nothing under the skills root", () => {
    expect(decide(
      { name: "file_read", input: { path: "skills/shortfall/SKILL.md" } },
      { name: "skill_used", input: { path: "/sandbox/org-1/skills/shortfall/SKILL.md" } },
      { name: "mcp_neko_graphjin_query", input: { query: "{ orders { id } }" } },
      { name: "file_write", input: { path: "/sandbox/org-1/runs/run-1/artifacts/report.md", content: "x" } },
      { name: "edit", input: { title: "write: runs/run-1/notes.md", locations: [{ path: "runs/run-1/notes.md" }] } },
    )).toEqual({ capture: false, reason: "no_skill_writes" });
    expect(decide()).toEqual({ capture: false, reason: "no_skill_writes" });
  });

  it("captures a file write or edit under the skills root", () => {
    expect(decide({ name: "file_write", input: { path: "skills/shortfall/notes.md", content: "x" } }).reason).toBe("skill_write");
    expect(decide({ name: "file_edit", input: { path: "/sandbox/org-1/runs/../skills/a/SKILL.md" } }).reason).toBe("skill_write");
    expect(decide({ name: "edit", input: { rawInput: { path: "/sandbox/org-1/skills/a/SKILL.md" } } }).reason).toBe("skill_write");
  });

  it("captures after any shell or terminal command", () => {
    expect(decide({ name: "terminal", input: { command: "ls" } })).toEqual({ capture: true, reason: "shell" });
    expect(decide({ name: "execute", input: { title: "terminal: ls" } }).reason).toBe("shell");
    expect(decide({ name: "delegate_task" }).reason).toBe("shell");
  });

  it("captures after the skill builder or skill_manage", () => {
    expect(decide({ name: "mcp_neko_skills_create_skill", input: { name: "new" } }).reason).toBe("skill_tool");
    expect(decide({ name: "create_skill" }).reason).toBe("skill_tool");
    expect(decide({ name: "edit", input: { title: "skill edit: shortfall/references/a.md" } }).reason).toBe("skill_tool");
  });

  it("captures when the evidence is missing or a write has no readable path", () => {
    expect(skillCaptureDecision(null)).toEqual({ capture: true, reason: "evidence_missing" });
    expect(decide({ name: "file_write", input: "{\"content\":\"truncated" }).reason).toBe("write_path_unknown");
    expect(decide({ name: "patch", input: { rawInput: { mode: "patch" } } }).reason).toBe("write_path_unknown");
  });
});
