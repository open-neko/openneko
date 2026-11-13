import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { cp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { diffSkillTrees, hashSkillTree, skillCaptureDecision, stageOwnSkills } from "../src/config-vcs/skill-capture";
import {
  matchOwnSkills,
  memberFolderName,
  memberHandle,
  memberSkillsRoot,
  ownRunSkills,
  privateCopyName,
  runSkillsWithOwn,
} from "../src/work/member-skills";
import {
  bundledSkillDir,
  copySkillOverrides,
  fingerprintSkillTree,
  materializeBuiltinSkills,
  SKILL_ORIGIN_FILE,
} from "../src/work/workspace";

// The Go turn stage (apps/openneko/internal/webfront/turnstage.go and
// turnskills.go) must stage a run's skills and decide on and find skill
// edits as the Node launcher does. Run with UPDATE_PROMPT_FIXTURES=1 to
// rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/skillstage.json");

const CWD = "/runs/s/workspace/org-1";
const ROOTS = ["/runs/s/workspace/org-1/skills"];

const DECISIONS: Array<Array<{ name: string; input?: unknown }> | null> = [
  null,
  [],
  [{ name: "read_file", input: { path: "skills/a/SKILL.md" } }],
  [{ name: "mcp__neko__create_skill" }],
  [{ name: "neko_skill_manage", input: {} }],
  [{ name: "xcreate_skill" }],
  [{ name: "terminal", input: { command: "ls" } }],
  [{ name: "delegate_task" }],
  [{ name: "file_write", input: { title: "skill report" } }],
  [{ name: "file_write", input: {} }],
  [{ name: "write_file", input: { path: "notes/a.md" } }],
  [{ name: "write_file", input: { path: "~/x" } }],
  [{ name: "edit", input: { path: "skills/a/SKILL.md" } }],
  [{ name: "edit", input: { path: "/elsewhere/skills/a.md" } }],
  [{ name: "patch", input: { rawInput: { path: "/runs/s/workspace/org-1/skills" } } }],
  [{ name: "patch", input: { locations: [{ path: "a.md" }, { path: "../org-1/skills/b/c.md" }] } }],
  [{ name: "move", input: { path: "/runs/s/workspace/org-1/skillset/a.md" } }],
  [{ name: "delete", input: { path: "/runs/s/workspace/org-1/skills/../notes.md" } }],
  [{ name: "delete", input: { path: "" } }],
  [{ name: "move", input: { path: 7 } }],
  [{ name: "file_edit", input: { path: "notes/a.md" } }, { name: "process" }],
];

const DIFFS: Array<[Record<string, string>, Record<string, string>]> = [
  [{ "a/SKILL.md": "1" }, { "a/SKILL.md": "1" }],
  [{ "a/SKILL.md": "1" }, { "a/SKILL.md": "2", "a/new.py": "3" }],
  [{ "a/SKILL.md": "1", "a/old.py": "2", "b/SKILL.md": "3" }, { "a/SKILL.md": "1" }],
  [{}, { "top.md": "1", "a/__pycache__/x.pyc": "2", "a/._x": "3", "a/./x": "4", "a//x": "5", "b/SKILL.md": "6" }],
  [{ "z/SKILL.md": "1", "a/SKILL.md": "1" }, { "z/SKILL.md": "2", "a/SKILL.md": "3", "m/x.md": "4" }],
];

const TREE: Record<string, string> = {
  "README.md": "top level\n",
  "report/SKILL.md": "# Report\n",
  "report/scripts/run.py": "print(1)\n",
  "report/__pycache__/run.cpython.pyc": "x",
  "report/.DS_Store": "x",
  "report/._SKILL.md": "x",
  "report/scripts/run.pyc": "x",
  "other/deep/a/b.txt": "b\n",
};

type Step =
  | { builtin: string; origin: boolean }
  | { write: string; text: string }
  | { overlay: string; text: string };

const BUILTIN = "graphjin-config";
const REPORT_SKILL = "---\nname: report\n---\nWrite the report.\n\n<!-- openneko-learned:start -->\n## Learned guidance\n\nOld.\n<!-- openneko-learned:end -->\n\nTail.\n";

type Stage = { steps: Step[]; allowed: string[] | null; own?: Record<string, string>; ownNames?: string[] };

const STAGES: Stage[] = [
  { steps: [{ builtin: BUILTIN, origin: true }], allowed: null },
  { steps: [{ builtin: BUILTIN, origin: false }], allowed: null },
  { steps: [{ builtin: BUILTIN, origin: true }, { write: `${BUILTIN}/SKILL.md`, text: "Changed.\n" }], allowed: null },
  { steps: [{ builtin: BUILTIN, origin: true }, { overlay: BUILTIN, text: "---\nbase_hash: BASE\nstatus: applied\n---\n\n Use the dev config. \n" }], allowed: null },
  { steps: [{ builtin: BUILTIN, origin: false }, { overlay: BUILTIN, text: "---\nbase_hash: BASE\nstatus: stale\n---\nStale.\n" }], allowed: null },
  {
    steps: [
      { builtin: BUILTIN, origin: true },
      { write: "report/SKILL.md", text: REPORT_SKILL },
      { write: "report/scripts/run.py", text: "print(2)\n" },
      { overlay: "report", text: "---\nbase_hash: BASE\n---\nPrefer net revenue.\n" },
      { write: "hidden/SKILL.md", text: "# Hidden\n" },
      { write: "pdf/SKILL.md", text: "# Custom pdf\n" },
      { write: "notes.md", text: "not a skill\n" },
    ],
    allowed: ["report", "pdf"],
  },
  {
    steps: [
      { write: "report/SKILL.md", text: "No end marker\n<!-- openneko-learned:start -->\nold\n" },
      { overlay: "report", text: "---\nbase_hash: BASE\nstatus: applied\n---\n   \n" },
      { write: "plain/SKILL.md", text: "# Plain\n" },
      { overlay: "plain", text: "---\nbase_hash: BASE\nstatus: disabled\n---\nOff.\n" },
    ],
    allowed: null,
  },
  { steps: [{ write: "report/SKILL.md", text: "# Report\n" }], allowed: [] },
  {
    steps: [
      { write: "report/SKILL.md", text: "# Team report\n" },
      { write: "report/scripts/run.py", text: "print(1)\n" },
      { write: "other/SKILL.md", text: "# Other\n" },
    ],
    own: {
      "report/SKILL.md": "# My report\n",
      "report/__pycache__/x.pyc": "x",
      "mine/SKILL.md": "# Mine\n",
      "outside/SKILL.md": "# Outside\n",
      "pdf/SKILL.md": "# My pdf\n",
      "empty/.keep/x.pyc": "x",
    },
    ownNames: ["report", "mine", "outside", "pdf", "empty", "../other"],
    allowed: ["report", "mine", "other", "pdf", "empty", "../other"],
  },
  {
    steps: [{ write: "report/SKILL.md", text: "# Team report\n" }],
    own: { "mine/SKILL.md": "# Mine\n" },
    ownNames: ["mine"],
    allowed: null,
  },
];

const OWN_FILES: Record<string, string> = {
  "a/SKILL.md": "---\nname: a\ndescription: A guide.\n---\n",
  "b/notes.md": "no front matter\n",
  "Bad Name/x.md": "x",
  "c/deep/x.md": "x",
};

const SKILLS = [
  { name: "report", description: "Monthly revenue report" },
  { name: "graphjin-config", description: "Edit sources" },
  { name: "pdf", description: "Make a PDF file" },
];

const NAMES = {
  folders: ["member-1", "u@x.com", "--", "", "Ab_c-9"].map((userId) => ({ userId, folder: memberFolderName(userId) })),
  handles: [
    ["Ann.Lee@example.com", "u1"],
    ["", "User_42"],
    [null, "!!!"],
    ["a-very-long-local-part-name@example.com", "u2"],
    ["--@x", "u3"],
  ].map(([email, userId]) => ({ email, userId, handle: memberHandle(email, userId as string) })),
  copies: [
    { skill: "report", handle: "ann", taken: [] as string[] },
    { skill: "report", handle: "ann", taken: ["report-ann", "report-ann-2"] },
    { skill: "Big_Skill", handle: "ann", taken: [] },
    { skill: "x".repeat(70), handle: "ann", taken: [] },
    { skill: "x".repeat(70), handle: "ann", taken: [`${"x".repeat(60)}-ann`] },
  ].map((c) => ({ ...c, name: privateCopyName(c.skill, c.handle, (name) => c.taken.includes(name)) })),
  matches: ["revenue report", "the PDF", "make a file for the month", "xx", ""].map((query) => ({
    query,
    names: matchOwnSkills(SKILLS, query).map((skill) => skill.name),
  })),
  scopes: [
    { allowed: null, admin: false, terminal: false },
    { allowed: ["report", "other"], admin: false, terminal: true },
    { allowed: [], admin: true, terminal: true },
  ].map((c) => ({
    ...c,
    out: runSkillsWithOwn(
      [{ name: "report", description: "Team" }, { name: "other", description: "Other" }],
      c.allowed ?? undefined,
      SKILLS,
      c.admin,
      c.terminal,
    ),
  })),
};

async function stage({ steps, allowed, own, ownNames }: Stage): Promise<{ tree: Record<string, string>; baseline: Record<string, string> }> {
  const root = mkdtempSync(join(tmpdir(), "skill-stage-fixture-"));
  const sorted = (map: Map<string, string>) =>
    Object.fromEntries([...map].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
  try {
    const org = join(root, "org");
    const skills = join(org, "skills");
    mkdirSync(skills, { recursive: true });
    for (const step of steps) {
      if ("builtin" in step) {
        await cp(bundledSkillDir(step.builtin), join(skills, step.builtin), { recursive: true });
        if (step.origin) {
          const sourceHash = await fingerprintSkillTree(bundledSkillDir(step.builtin));
          writeFileSync(join(skills, step.builtin, SKILL_ORIGIN_FILE), `${JSON.stringify({ kind: "builtin", sourceHash })}\n`);
        }
      } else if ("write" in step) {
        mkdirSync(dirname(join(skills, step.write)), { recursive: true });
        writeFileSync(join(skills, step.write), step.text);
      } else {
        const base = await fingerprintSkillTree(join(skills, step.overlay));
        mkdirSync(join(org, "skill-overlays", step.overlay), { recursive: true });
        writeFileSync(join(org, "skill-overlays", step.overlay, "LEARNED.md"), step.text.replace("BASE", base));
      }
    }
    const ownRoot = memberSkillsRoot(org, "member-1");
    for (const [path, text] of Object.entries(own ?? {})) {
      mkdirSync(dirname(join(ownRoot, path)), { recursive: true });
      writeFileSync(join(ownRoot, path), text);
    }
    const dest = join(root, "stage", "skills");
    await copySkillOverrides(skills, dest, [], allowed ?? undefined, false);
    await materializeBuiltinSkills(dest, allowed ?? undefined);
    const baseline = await hashSkillTree(skills);
    await stageOwnSkills({
      ownRoot,
      skillsRoot: dest,
      own: ownNames ?? [],
      ...(allowed ? { allowed } : {}),
      terminal: false,
      baseline,
    });
    return { tree: sorted(await hashSkillTree(dest)), baseline: sorted(baseline) };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

describe("Go turn stage fixtures", () => {
  it("matches the stored fixture", async () => {
    const root = mkdtempSync(join(tmpdir(), "skill-tree-fixture-"));
    try {
      for (const [path, text] of Object.entries(TREE)) {
        mkdirSync(dirname(join(root, path)), { recursive: true });
        writeFileSync(join(root, path), text);
      }
      const tree = Object.fromEntries([...(await hashSkillTree(root))].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
      const ownOrg = join(root, "own-org");
      for (const [path, text] of Object.entries(OWN_FILES)) {
        mkdirSync(dirname(join(memberSkillsRoot(ownOrg, "member-1"), path)), { recursive: true });
        writeFileSync(join(memberSkillsRoot(ownOrg, "member-1"), path), text);
      }
      mkdirSync(join(memberSkillsRoot(ownOrg, "member-1"), "empty", "sub"), { recursive: true });
      const ownList = await ownRunSkills(ownOrg, "member-1");
      const stages = [];
      for (const c of STAGES) stages.push({ ...c, ...(await stage(c)) });
      const fixture = `${JSON.stringify(
        {
          cwd: CWD,
          roots: ROOTS,
          decisions: DECISIONS.map((toolCalls) => ({
            toolCalls,
            ...skillCaptureDecision(toolCalls ? { toolCalls, cwd: CWD, skillsRoots: ROOTS } : null),
          })),
          diffs: DIFFS.map(([baseline, box]) => ({
            baseline,
            box,
            ...diffSkillTrees(new Map(Object.entries(baseline)), new Map(Object.entries(box))),
          })),
          treeFiles: TREE,
          tree,
          stages,
          names: NAMES,
          ownFiles: OWN_FILES,
          ownList,
        },
        null,
        2,
      )}\n`;
      if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
      expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
