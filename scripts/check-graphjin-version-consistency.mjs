import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const expectedSource = await readFile(
  resolve(root, "packages/llm/src/graphjin/version.ts"),
  "utf8",
);
const expected = /GRAPHJIN_VERSION\s*=\s*["'](\d+\.\d+\.\d+)["']/.exec(
  expectedSource,
)?.[1];
if (!expected) throw new Error("could not read the GraphJin source-of-truth version");

const files = [
  "Dockerfile",
  "scripts/install-clis.sh",
  "compose.adventureworks.eval.yml",
  ".github/workflows/pr-checks.yml",
  "packages/llm/src/graphjin/version.ts",
  "packages/records/src/graphjin/config.ts",
];
const versionPattern = /\b3\.\d+\.\d+\b/g;
const mismatches = [];
for (const file of files) {
  const content = await readFile(resolve(root, file), "utf8");
  for (const [index, line] of content.split("\n").entries()) {
    if (!/graphjin/i.test(line)) continue;
    for (const value of line.match(versionPattern) ?? []) {
      if (value !== expected) {
        mismatches.push(`${file}:${index + 1}: ${value} (expected ${expected})`);
      }
    }
  }
}
if (mismatches.length) {
  console.error("GraphJin version pins disagree:");
  console.error(mismatches.join("\n"));
  process.exit(1);
}
console.log(`GraphJin version pins are consistent: ${expected}`);
