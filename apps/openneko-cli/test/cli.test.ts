import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { runCli } from "../src/cli";
import {
  PLUGIN_MANIFEST_FILE,
  PLUGIN_MANIFEST_SCHEMA_URL,
} from "@open-neko/plugin-install";
import { writeStore } from "../src/marketplace-store";

const INTEGRITY = "sha512-" + "a".repeat(86) + "==";

function captureLines() {
  const stdout: string[] = [];
  const stderr: string[] = [];
  return {
    stdout: (s: string) => stdout.push(s),
    stderr: (s: string) => stderr.push(s),
    out: () => stdout.join("\n"),
    err: () => stderr.join("\n"),
  };
}

describe("runCli", () => {
  let cwd: string;
  let configDir: string;

  beforeEach(async () => {
    cwd = await mkdtemp(path.join(tmpdir(), "cli-runcli-"));
    configDir = await mkdtemp(path.join(tmpdir(), "cli-config-"));
  });

  afterEach(async () => {
    await rm(cwd, { recursive: true, force: true });
    await rm(configDir, { recursive: true, force: true });
  });

  it("--version prints version, exit 0", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["--version"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(0);
    expect(cap.out()).toMatch(/\d+\.\d+\.\d+/);
  });

  it("--help mentions marketplace commands", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["--help"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(0);
    expect(cap.out()).toMatch(/marketplace list/);
    expect(cap.out()).toMatch(/marketplace add/);
  });

  it("unknown command exits with code 2", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["frobnicate"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(2);
  });

  it("marketplace list shows the auto-added official marketplace", async () => {
    const cap = captureLines();
    await runCli({
      argv: ["marketplace", "list"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(cap.out()).toMatch(/official.*\[official\]/s);
  });

  it("marketplace add without URL exits 2 with a message", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["marketplace", "add"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(2);
    expect(cap.err()).toMatch(/URL required/);
  });

  it("marketplace remove refuses to drop official", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["marketplace", "remove", "official"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(1);
    expect(cap.err()).toMatch(/refusing to remove/);
  });

  it("install with no name exits 2", async () => {
    const cap = captureLines();
    const code = await runCli({
      argv: ["install"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(code).toBe(2);
    expect(cap.err()).toMatch(/package name required/);
  });

  it("init → list flow reports zero plugins", async () => {
    const cap = captureLines();
    await runCli({
      argv: ["init"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    await runCli({
      argv: ["list"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(cap.out()).toMatch(/no plugins installed/);
  });

  it("list shows the marketplace field when present", async () => {
    await writeFile(
      path.join(cwd, PLUGIN_MANIFEST_FILE),
      JSON.stringify({
        schema: PLUGIN_MANIFEST_SCHEMA_URL,
        plugins: [
          {
            name: "@open-neko/plugin-parallel-search",
            version: "0.2.0",
            integrity: INTEGRITY,
            capabilities: { network: ["search.parallel.ai"] },
            marketplace: "official",
          },
        ],
      }),
      "utf8",
    );
    const cap = captureLines();
    await runCli({
      argv: ["list"],
      cwd,
      configDir,
      stdout: cap.stdout,
      stderr: cap.stderr,
    });
    expect(cap.out()).toContain("@open-neko/plugin-parallel-search@0.2.0");
    expect(cap.out()).toContain("from=official");
  });

  // Sanity that writeStore exists in scope (silences unused-import lint).
  it("marketplace store is writable in tests", async () => {
    await writeStore({ marketplaces: [] }, configDir);
  });
});
