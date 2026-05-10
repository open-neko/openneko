import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { ensureOrgWorkspace, ensureWorkWorkspace } from "../src/work/workspace";

const cleanupPaths: string[] = [];

afterEach(async () => {
  while (cleanupPaths.length > 0) {
    const path = cleanupPaths.pop();
    if (path) await rm(path, { recursive: true, force: true });
  }
  delete process.env.HOME;
});

describe("work workspace", () => {
  it("seeds built-in skills into the org workspace", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const workspace = await ensureOrgWorkspace("org-test");
    const skills = await readdir(workspace.skillsRoot);
    expect(skills).toContain("skill-creator");
    expect(skills).toContain("pdf");
  }, 30_000);

  it("creates per-thread and per-run directories", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-home-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const workspace = await ensureWorkWorkspace("org-test", "thread-1", "run-1");
    expect(workspace.threadUploadsRoot).toContain("thread-1");
    expect(workspace.artifactRoot).toContain("run-1");
  }, 30_000);
});
