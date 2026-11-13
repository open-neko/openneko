import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { priceUsage } from "../src/spend/ledger";
import { fingerprintEffectiveSkill } from "../src/work/skill-overlay";
import { detectSkillUse } from "../src/work/skill-usage";
import type { AgentEvent } from "../src/agent-backend";

// The Go run event store (apps/openneko/internal/webfront/turnrun.go) must
// price a turn, find a skill read and hash a skill as Node does. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/runstore.json");

const DETECT: unknown[] = [
  { type: "tool_start", id: "1", name: "read_file", input: { path: "/home/a/skills/xlsx/SKILL.md" } },
  { type: "tool_start", id: "2", name: "shell", input: { command: "cat skills\\monthly-report\\SKILL.md" } },
  { type: "tool_start", id: "3", name: "read_file", input: { path: "skills/xlsx/SKILL.md?raw=1" } },
  { type: "tool_start", id: "4", name: "read_file", input: { path: "skills/xlsx/SKILL.md.bak" } },
  { type: "tool_start", id: "5", name: "read_file", input: { path: "Skills/XLSX/skill.md" } },
  { type: "tool_start", id: "6", name: "read_file", input: { path: "skills/Mixed-Case/SKILL.md" } },
  { type: "tool_start", id: "7", name: "x", input: { b: "skills/second/SKILL.md", a: "skills/first/SKILL.md", 1: "skills/numbered/SKILL.md" } },
  { type: "tool_start", id: "8", name: "x", input: { a: { b: { c: { d: { e: { f: "skills/deep/SKILL.md" } } } } } } },
  { type: "tool_start", id: "9", name: "x", input: { a: { b: { c: { d: { e: { f: { g: "skills/deeper/SKILL.md" } } } } } } } },
  { type: "tool_start", id: "10", name: "x", input: [["skills/in-list/SKILL.md"]] },
  { type: "tool_start", id: "11", name: "x", input: { path: "myskills/xlsx/SKILL.md" } },
  { type: "tool_start", id: "12", name: "x" },
  { type: "tool_end", id: "13", name: "read_file", result: "skills/xlsx/SKILL.md" },
  { type: "tool_start", id: "14", name: "x", input: { path: "skills/a--b/SKILL.md", other: "skills/ok-2/SKILL.md#top" } },
];

const SKILL_FILES: Record<string, string> = {
  "SKILL.md": "---\nname: report\n---\nWrite the report.\n",
  "scripts/run.js": "console.log(1)\n",
};

const OVERLAYS: Array<string | null> = [
  null,
  "---\nskill_name: report\nbase_hash: BASE\nstatus: applied\n---\n\n Prefer net revenue. \n",
  "---\nskill_name: report\nbase_hash: BASE\n---\nNo status line.\n",
  "---\nbase_hash: BASE\nstatus: stale\n---\nStale guidance.\n",
  "---\nbase_hash: BASE\nstatus: disabled\n---\nOff.\n",
  "---\nbase_hash: other\nstatus: applied\n---\nOther base.\n",
  "---\nstatus: applied\n---\nNo base.\n",
  "---\r\nbase_hash: BASE\r\nstatus: applied  \r\n---\r\nWindows lines.\r\n",
  "---\nbase_hash: BASE\nstatus: maybe\n---\nUnknown status.\n",
  "No front matter at all.\n",
  "---\nbase_hash:\n  BASE\nstatus: applied\n---\nValue on the next line.\n",
];

const USAGE: Array<[Record<string, unknown>, number]> = [
  [{ costStatus: "estimated", estimatedCostUsd: 0.0123456 }, 5_000_000],
  [{ costStatus: "billed", billedCostUsd: 0.5, estimatedCostUsd: 0.7 }, 5_000_000],
  [{ costStatus: "included", estimatedCostUsd: 0 }, 5_000_000],
  [{ costStatus: "unknown", estimatedCostUsd: 0.2 }, 5_000_000],
  [{ costStatus: "estimated" }, 2_000_000],
  [{}, 0],
  [{ costStatus: "estimated", estimatedCostUsd: -1 }, 5_000_000],
  [{ billedCostUsd: 0.0000001 }, 5_000_000],
];

describe("Go run event store fixtures", () => {
  it("matches the stored fixture", async () => {
    const root = mkdtempSync(join(tmpdir(), "run-store-fixture-"));
    try {
      const base = join(root, "skills", "report");
      for (const [path, text] of Object.entries(SKILL_FILES)) {
        mkdirSync(dirname(join(base, path)), { recursive: true });
        writeFileSync(join(base, path), text);
      }
      const baseHash = await fingerprintEffectiveSkill(root, "report", base);
      const overlays = [];
      for (const overlay of OVERLAYS) {
        const dir = join(root, "skill-overlays", "report");
        rmSync(dir, { recursive: true, force: true });
        const text = overlay?.replaceAll("BASE", baseHash) ?? null;
        if (text !== null) {
          mkdirSync(dir, { recursive: true });
          writeFileSync(join(dir, "LEARNED.md"), text);
        }
        overlays.push({ overlay, hash: await fingerprintEffectiveSkill(root, "report", base) });
      }
      const fixture = `${JSON.stringify(
        {
          detect: DETECT.map((event) => ({ event, skill: detectSkillUse(event as AgentEvent)?.name ?? null })),
          skillFiles: SKILL_FILES,
          baseHash,
          overlays,
          usage: USAGE.map(([usage, cap]) => ({ usage, cap, ...priceUsage(usage as never, cap) })),
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
