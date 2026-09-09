import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parse as parseYaml } from "yaml";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const specsRoot = resolve(here, "../../../packs/google-workspace/graphjin/specs");
const services = ["gmail", "drive", "calendar", "sheets", "docs", "slides"];

describe("Google Workspace curated REST specifications", () => {
  it.each(services)("keeps %s read-only and bearer-authenticated", async (service) => {
    const spec = parseYaml(await readFile(resolve(specsRoot, `${service}.yaml`), "utf8")) as {
      openapi: string;
      paths: Record<string, Record<string, unknown>>;
      components: { securitySchemes: { oauthBearer: { type: string; scheme: string } } };
    };

    expect(spec.openapi).toBe("3.0.3");
    expect(spec.components.securitySchemes.oauthBearer).toEqual({
      type: "http",
      scheme: "bearer",
    });
    expect(Object.keys(spec.paths).length).toBeGreaterThan(0);
    for (const operations of Object.values(spec.paths)) {
      expect(Object.keys(operations)).toEqual(["get"]);
      expect(operations.get).toMatchObject({ operationId: expect.any(String) });
    }
  });

  it("keeps the initial skills REST-based and read-only", async () => {
    const skillsRoot = resolve(specsRoot, "../../skills");
    const skills = ["google-workspace-read", "google-workspace-cross-app-research"];
    for (const skill of skills) {
      const content = await readFile(resolve(skillsRoot, skill, "SKILL.md"), "utf8");
      expect(content).not.toMatch(/\bgws\s+(?:gmail|drive|calendar|sheets|docs|slides)\b/i);
      expect(content).toMatch(/read-only|does not perform writes/i);
    }
  });
});
