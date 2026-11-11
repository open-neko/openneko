import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { readGraphjinConfigFileView } from "../src/graphjin/config-file-view";

const PLANTED = "Planted-Pw-7f3a9c";

let dir: string;
let file: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "gj-view-"));
  file = join(dir, "agentic.yml");
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

describe("readGraphjinConfigFileView", () => {
  it("hides every planted secret and keeps the rest", async () => {
    await writeFile(
      file,
      [
        `# old password ${PLANTED}`,
        "app_name: Store",
        "auth:",
        "  type: jwt",
        "  jwt:",
        `    secret: ${PLANTED}`,
        "databases:",
        "  shop:",
        "    type: postgres",
        "    host: db.internal",
        "    user: reader",
        `    password: ${PLANTED}`,
        `    connection_string: postgres://reader:${PLANTED}@db.internal/shop`,
        `    note: mysql://reader:${PLANTED}@db.internal/shop`,
        "",
      ].join("\n"),
    );
    await writeFile(
      join(dir, ".openneko-graphjin-state"),
      JSON.stringify({
        state: "restarting",
        reason: `dial postgres://reader:${PLANTED}@db.internal failed`,
        since: "2026-11-11T08:00:00Z",
        restarts: 2,
      }),
    );

    const view = await readGraphjinConfigFileView(file);

    expect(view.error).toBeNull();
    expect(view.path).toBe(file);
    expect(JSON.stringify(view)).not.toContain(PLANTED);
    expect(view.yaml).toContain("host: db.internal");
    expect(view.yaml).toContain("password: \"[REDACTED]\"");
    expect(view.state).toMatchObject({ state: "restarting", restarts: 2 });
    expect(view.state?.reason).toContain("[REDACTED]@db.internal");
  });

  it("names the problem when the file is missing or not set", async () => {
    expect(await readGraphjinConfigFileView(file)).toMatchObject({
      path: file,
      yaml: null,
      error: "the file does not exist",
      state: null,
    });
    expect(await readGraphjinConfigFileView("")).toMatchObject({
      path: null,
      error: "OPENNEKO_GRAPHJIN_CONFIG is not set",
    });
  });

  it("shows no source text when the YAML does not parse", async () => {
    await writeFile(file, `password: ${PLANTED}\n  bad: [\n`);
    const view = await readGraphjinConfigFileView(file);
    expect(view.yaml).toBeNull();
    expect(view.error).toMatch(/not valid YAML/);
    expect(JSON.stringify(view)).not.toContain(PLANTED);
  });
});
