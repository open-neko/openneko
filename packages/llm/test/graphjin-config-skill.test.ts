import { readdir, readFile, stat } from "node:fs/promises";
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

  it("names every built-in pack that carries a ready source config", async () => {
    const packsRoot = resolve(__dirname, "../../../packs");
    const withSources: string[] = [];
    for (const entry of await readdir(packsRoot, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      if (await stat(resolve(packsRoot, entry.name, "graphjin/sources.yaml")).then(() => true, () => false)) {
        withSources.push(entry.name);
      }
    }
    expect(withSources.length).toBeGreaterThan(0);
    const skill = await readFile(resolve(skillRoot, "SKILL.md"), "utf8");
    const tools = await readFile(resolve(__dirname, "../src/work/tools.ts"), "utf8");
    const toolText = tools.slice(tools.indexOf('"present_pack_setup"'), tools.indexOf("{ packId: PACK_ID_SCHEMA }"));
    const listed = (text: string) => [...text.matchAll(/`?\b([a-z]+(?:-[a-z]+)*)\b`?/g)].map((match) => match[1]);
    for (const pack of withSources) {
      expect(skill).toContain(`\`${pack}\``);
      expect(listed(toolText)).toContain(pack);
    }
  });
});
