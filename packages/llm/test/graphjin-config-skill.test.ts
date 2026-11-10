import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { GRAPHJIN_VERSION } from "../src/graphjin/version";

const skillRoot = resolve(__dirname, "../assets/builtin-skills/graphjin-config");

describe("graphjin-config skill", () => {
  it("names the pinned GraphJin version in its config reference", async () => {
    const goMod = await readFile(resolve(__dirname, "../../../apps/openneko/go.mod"), "utf8");
    const pinned = goMod.match(/^\s*github\.com\/dosco\/graphjin\/core\/v3 v(\S+)$/m)?.[1];
    expect(pinned).toBe(GRAPHJIN_VERSION);

    const reference = await readFile(resolve(skillRoot, "references/config-reference.md"), "utf8");
    expect(reference).toContain(`This reference describes GraphJin v${pinned}.`);
    const named = [...reference.matchAll(/GraphJin v(\d+\.\d+\.\d+)/g)].map((m) => m[1]);
    expect(new Set(named)).toEqual(new Set([pinned]));
  });

  it("works from the redacted config and never calls GraphJin's own agent", async () => {
    const skill = await readFile(resolve(skillRoot, "SKILL.md"), "utf8");
    expect(skill).toContain("describe_source_config");
    expect(skill).toContain("references/config-reference.md");
    expect(skill).not.toMatch(/ask_graphjin|config agent|configuration agent/i);
  });
});
