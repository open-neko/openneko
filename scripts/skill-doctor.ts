#!/usr/bin/env -S node --experimental-strip-types
// Report which bundled-skill deps are present locally; on Linux with --install,
// run apt-get + pip. Mirrors Reckon's scripts/doctor.ts, scoped to Neko's
// monorepo layout. Dockerfile installs everything at build time; this script
// covers dev-machine setup and a deploy gate.
//
//   pnpm skills:check               # report
//   pnpm skills:check --install     # Linux only: apt + pip the missing bits
import { execSync } from "node:child_process";
import { readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  KNOWN_SKILL_DEPS,
  aggregateSkillDeps,
} from "../packages/llm/src/work/skill-deps.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const BUILTIN_SKILLS_ROOT = resolve(
  HERE,
  "..",
  "packages",
  "llm",
  "assets",
  "builtin-skills",
);

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

const bundled = readdirSync(BUILTIN_SKILLS_ROOT, { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name);

const pythonPresent = which("python3");
const reports: { name: string; status: "ok" | "missing"; missing: string[] }[] = [];
const localMissingPip = new Set<string>();
const localMissingApt = new Set<string>();

for (const name of bundled) {
  const deps = KNOWN_SKILL_DEPS[name];
  if (!deps) continue;
  const missing: string[] = [];
  if (pythonPresent) {
    for (let i = 0; i < deps.python.length; i++) {
      if (!pythonHas(deps.python[i])) {
        missing.push(`py:${deps.python[i]}`);
        localMissingPip.add(deps.pip[i]);
      }
    }
  } else if (deps.python.length > 0) {
    missing.push("py:python3");
  }
  for (let i = 0; i < deps.binaries.length; i++) {
    if (!which(deps.binaries[i])) {
      missing.push(`bin:${deps.binaries[i]}`);
      localMissingApt.add(deps.apt[i] ?? deps.binaries[i]);
    }
  }
  reports.push({
    name,
    status: missing.length ? "missing" : "ok",
    missing,
  });
}

console.log("\nNeko bundled-skill dependency check");
console.log("───────────────────────────────────");
for (const r of reports) {
  const tag = r.status === "ok" ? "OK     " : "MISSING";
  const note = r.status === "missing" ? `  — ${r.missing.join(", ")}` : "";
  console.log(`  [${tag}] ${r.name}${note}`);
}
console.log("");

const allOk = reports.every((r) => r.status === "ok");
if (allOk) {
  console.log("All bundled skills are ready.\n");
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
    console.log("Nothing to install.\n");
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
console.log("Install commands for a fresh machine:\n");
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
console.log(isLinux ? "Run `pnpm skills:install` to do it automatically.\n" : "");
process.exit(1);
