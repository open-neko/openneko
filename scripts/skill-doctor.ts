#!/usr/bin/env -S node --experimental-strip-types
// Report which skill deps are present locally. Walks two roots:
//
//   1. packages/llm/assets/builtin-skills/   — first-party skills the
//      Dockerfile bakes into the image. Driven by the hand-maintained
//      KNOWN_SKILL_DEPS manifest.
//
//   2. ~/.openneko/skills/                   — community skills the
//      operator installed via `openneko install <git-url>` (M7). Deps
//      come from each SKILL.md's frontmatter `prerequisites.commands`;
//      we don't try to map a binary name to an apt package, so the
//      doctor only reports presence — operators install community-skill
//      binaries themselves.
//
//   pnpm skills:check               # report
//   pnpm skills:check --install     # Linux only: apt + pip the missing
//                                   #  bundled skill bits (community
//                                   #  skill installs are operator-driven)
import { execSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  KNOWN_SKILL_DEPS,
  aggregateSkillDeps,
  synthesizeSkillDeps,
  type SkillDeps,
} from "../packages/llm/src/work/skill-deps.ts";
import { parseSkillFrontmatter } from "../packages/llm/src/work/skill-frontmatter.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const BUILTIN_SKILLS_ROOT = resolve(
  HERE,
  "..",
  "packages",
  "llm",
  "assets",
  "builtin-skills",
);
const INSTALLED_SKILLS_ROOT =
  process.env.OPENNEKO_SKILLS_DIR ?? join(homedir(), ".openneko", "skills");

const isMac = process.platform === "darwin";
const isLinux = process.platform === "linux";
const flagInstall = process.argv.includes("--install");

function which(cmd: string): boolean {
  try {
    execSync(`command -v ${cmd}`, { stdio: "ignore", shell: "/bin/sh" });
    return true;
  } catch {
    return false;
  }
}

function pythonHas(mod: string): boolean {
  try {
    execSync(`python3 -c 'import ${mod}'`, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function readSkillDirs(root: string): string[] {
  if (!existsSync(root)) return [];
  return readdirSync(root, { withFileTypes: true })
    .filter((d) => d.isDirectory())
    .map((d) => d.name);
}

function depsForInstalledSkill(skillDir: string): SkillDeps | null {
  const path = join(skillDir, "SKILL.md");
  if (!existsSync(path)) return null;
  let content: string;
  try {
    content = readFileSync(path, "utf8");
  } catch {
    return null;
  }
  const frontmatter = parseSkillFrontmatter(content);
  if (!frontmatter.prerequisites) {
    return synthesizeSkillDeps({});
  }
  return synthesizeSkillDeps(frontmatter.prerequisites);
}

const pythonPresent = which("python3");

interface SkillReport {
  name: string;
  source: "bundled" | "installed";
  status: "ok" | "missing";
  missing: string[];
}

const reports: SkillReport[] = [];
const localMissingPip = new Set<string>();
const localMissingApt = new Set<string>();

function checkDeps(name: string, source: "bundled" | "installed", deps: SkillDeps): SkillReport {
  const missing: string[] = [];
  if (pythonPresent) {
    for (let i = 0; i < deps.python.length; i++) {
      if (!pythonHas(deps.python[i]!)) {
        missing.push(`py:${deps.python[i]}`);
        if (source === "bundled" && deps.pip[i]) {
          localMissingPip.add(deps.pip[i]!);
        }
      }
    }
  } else if (deps.python.length > 0) {
    missing.push("py:python3");
  }
  for (let i = 0; i < deps.binaries.length; i++) {
    if (!which(deps.binaries[i]!)) {
      missing.push(`bin:${deps.binaries[i]}`);
      if (source === "bundled" && (deps.apt[i] ?? deps.binaries[i])) {
        localMissingApt.add(deps.apt[i] ?? deps.binaries[i]!);
      }
    }
  }
  return { name, source, status: missing.length ? "missing" : "ok", missing };
}

// Bundled skills.
for (const name of readSkillDirs(BUILTIN_SKILLS_ROOT)) {
  const deps = KNOWN_SKILL_DEPS[name];
  if (!deps) continue;
  reports.push(checkDeps(name, "bundled", deps));
}

// Installed (community) skills.
for (const name of readSkillDirs(INSTALLED_SKILLS_ROOT)) {
  // Honour KNOWN_SKILL_DEPS if a bundled name was overridden by an
  // operator's install (unusual, but consistent semantics — the
  // operator's copy wins).
  const skillDir = join(INSTALLED_SKILLS_ROOT, name);
  const deps = KNOWN_SKILL_DEPS[name] ?? depsForInstalledSkill(skillDir);
  if (!deps) continue;
  reports.push(checkDeps(name, "installed", deps));
}

console.log("\nNeko skill dependency check");
console.log("───────────────────────────");
const bundled = reports.filter((r) => r.source === "bundled");
const installed = reports.filter((r) => r.source === "installed");
if (bundled.length > 0) {
  console.log("\nBundled (in image):");
  for (const r of bundled) printRow(r);
}
if (installed.length > 0) {
  console.log("\nInstalled under ~/.openneko/skills/:");
  for (const r of installed) printRow(r);
}
console.log("");

function printRow(r: SkillReport): void {
  const tag = r.status === "ok" ? "OK     " : "MISSING";
  const note = r.status === "missing" ? `  — ${r.missing.join(", ")}` : "";
  console.log(`  [${tag}] ${r.name}${note}`);
}

const allOk = reports.every((r) => r.status === "ok");
if (allOk) {
  console.log("All skills are ready.\n");
  process.exit(0);
}

if (flagInstall) {
  if (!isLinux) {
    console.log("--install is Linux-only. On macOS, run the brew commands below.\n");
    process.exit(1);
  }
  const aptList = [...localMissingApt];
  if (!pythonPresent) aptList.unshift("python3", "python3-pip");
  const pipList = [...localMissingPip];
  if (aptList.length === 0 && pipList.length === 0) {
    console.log("Nothing to auto-install — community skill binaries are operator-driven.\n");
    process.exit(0);
  }
  if (aptList.length > 0) {
    const cmd = `sudo apt-get install -y ${aptList.join(" ")}`;
    console.log(`$ ${cmd}\n`);
    execSync(cmd, { stdio: "inherit" });
  }
  if (pipList.length > 0) {
    const cmd = `python3 -m pip install --user --break-system-packages --no-warn-script-location ${pipList.join(" ")}`;
    console.log(`\n$ ${cmd}\n`);
    execSync(cmd, { stdio: "inherit" });
  }
  console.log("\nDone.\n");
  process.exit(0);
}

const agg = aggregateSkillDeps();
console.log("Install commands for a fresh machine (bundled skills only):\n");
if (isMac) {
  console.log("  # macOS");
  if (agg.brewFormulas.length > 0)
    console.log(`  brew install python ${agg.brewFormulas.join(" ")}`);
  if (agg.brewCasks.length > 0)
    console.log(`  brew install --cask ${agg.brewCasks.join(" ")}`);
  if (agg.pip.length > 0) console.log(`  pip3 install ${agg.pip.join(" ")}`);
  console.log("");
}
console.log("  # Linux (Debian/Ubuntu)");
console.log(`  sudo apt-get install -y python3 python3-pip ${agg.apt.join(" ")}`);
if (agg.pip.length > 0)
  console.log(`  pip3 install --user --break-system-packages ${agg.pip.join(" ")}`);
console.log("");

const missingCommunityBins = installed
  .filter((r) => r.status === "missing")
  .flatMap((r) => r.missing.filter((m) => m.startsWith("bin:")));
if (missingCommunityBins.length > 0) {
  console.log(
    "Community skills under ~/.openneko/skills/ declared binaries that aren't present:",
  );
  console.log(`  ${missingCommunityBins.join(", ")}`);
  console.log("Install those manually (they're not in any apt/brew package the doctor knows about).\n");
}

console.log(isLinux ? "Run `pnpm skills:install` to do it automatically.\n" : "");
process.exit(1);
