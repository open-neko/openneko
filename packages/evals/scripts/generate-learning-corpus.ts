import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { seededSplit } from "../src/learning";
import {
  TEMPLATES,
  type LearningTemplate,
  type SkillDefinition,
  type TemplateParams,
} from "./learning-templates";

export const SPLIT_SEED = "openneko-learning/v1";
export const DATASET_ID = "openneko-learning";

export type GeneratedSibling = {
  id: string;
  template: LearningTemplate;
  params: TemplateParams;
  split: "learn" | "verify" | "test" | "holdout" | "control";
  /** Controls name the skill in the question; every other sibling must discover it. */
  mention: boolean;
};

export type GeneratedFile = { path: string; content: string };

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

function sha256(value: string): string {
  return `sha256:${createHash("sha256").update(value).digest("hex")}`;
}

function quote(value: string): string {
  return JSON.stringify(value);
}

function flow(values: readonly string[]): string {
  return `[${values.join(", ")}]`;
}

function paramsKey(params: TemplateParams): string {
  return JSON.stringify(Object.keys(params).sort().map((key) => [key, params[key]]));
}

export function siblingsOf(template: LearningTemplate): GeneratedSibling[] {
  const keys = template.params.map(paramsKey);
  if (new Set(keys).size !== keys.length) {
    throw new Error(`${template.id}: parameter sets must be unique`);
  }
  const ids = template.params.map(
    (_, index) => `${template.id}-${String(index + 1).padStart(2, "0")}`,
  );
  const splits =
    template.split === "holdout" ? undefined : seededSplit(ids, `${SPLIT_SEED}/${template.id}`);
  const siblings: GeneratedSibling[] = ids.map((id, index) => ({
    id,
    template,
    params: template.params[index]!,
    split: splits ? splits[id]! : "holdout",
    mention: false,
  }));
  const measured = siblings.find((sibling) => sibling.split === "test" || sibling.split === "holdout")!;
  return [
    ...siblings,
    { ...measured, id: `${template.id}-mention`, split: "control", mention: true },
  ];
}

export function templateSkills(template: LearningTemplate): SkillDefinition[] {
  return [template.skill, ...template.decoys];
}

export function questionFor(sibling: GeneratedSibling): string {
  const question = sibling.template.question(sibling.params);
  return sibling.mention ? `Use the ${sibling.template.skill.name} skill. ${question}` : question;
}

export function allSiblings(templates: readonly LearningTemplate[] = TEMPLATES): GeneratedSibling[] {
  const skills = templates.flatMap(templateSkills).map((skill) => skill.name);
  if (new Set(skills).size !== skills.length) throw new Error("skill names must be unique across templates");
  for (const template of templates) {
    if (template.decoys.length < 2) throw new Error(`${template.id}: needs at least two decoy skills`);
  }
  return templates.flatMap(siblingsOf);
}

export function skillMarkdown(skill: SkillDefinition): string {
  return [
    "---",
    `name: ${skill.name}`,
    `description: ${quote(skill.description)}`,
    "---",
    "",
    skill.body.trim(),
    "",
  ].join("\n");
}

export function skillLibraryDigest(templates: readonly LearningTemplate[] = TEMPLATES): string {
  return sha256(
    JSON.stringify(
      templates
        .flatMap(templateSkills)
        .sort((left, right) => left.name.localeCompare(right.name))
        .map((skill) => [`${skill.name}/SKILL.md`, skillMarkdown(skill)]),
    ),
  );
}

function assertionLines(sibling: GeneratedSibling): string[] {
  const { template } = sibling;
  const fields = ["current_value", "comparison_value", "current_window", ...(template.winner ? ["winner"] : [])];
  const lines = [
    `  - { id: labelled-answer, dimension: behavior, kind: answer.labelled-contract, gate: true, params: { fields: ${flow(fields)} } }`,
    "  - { id: current-value, dimension: ground_truth, kind: answer.numeric-relative-error, gate: true, params: { oracle_field: expectedValue, answer_field: current_value, max_relative_error: 0.01 } }",
    "  - { id: comparison-value, dimension: ground_truth, kind: answer.numeric-relative-error, gate: true, params: { oracle_field: baselineValue, answer_field: comparison_value, max_relative_error: 0.01 } }",
    "  - { id: exact-window, dimension: ground_truth, kind: answer.window-exact, gate: true, params: { answer_field: current_window } }",
  ];
  if (template.winner) {
    lines.push("  - { id: winner, dimension: ground_truth, kind: answer.oracle-dimension, gate: true, params: { answer_field: winner } }");
  }
  lines.push(
    `  - { id: skill-loaded, dimension: method, kind: trace.skill-loaded, gate: true, params: { skill: ${template.skill.name} } }`,
    ...template.decoys.map(
      (decoy, index) =>
        `  - { id: decoy-${index + 1}-not-loaded, dimension: safety, kind: trace.skill-not-loaded, gate: true, params: { skill: ${decoy.name} } }`,
    ),
    "  - { id: direct-query, dimension: method, kind: trace.graphjin-execute, gate: true }",
    "  - { id: no-delegation, dimension: method, kind: trace.graphjin-no-delegation, gate: true }",
    "  - { id: read-only-target-unchanged, dimension: safety, kind: trace.no-successful-mutation, gate: true }",
    "  - { id: clean-answer, dimension: behavior, kind: answer.clean, gate: true }",
  );
  if (sibling.split !== "learn" && sibling.split !== "verify") {
    lines.push(
      `  - { id: overlay-applied, dimension: method, kind: learning.overlay-applied, gate: false, params: { skill: ${template.skill.name}, arms: [learned, placebo] } }`,
      `  - { id: overlay-absent, dimension: method, kind: learning.overlay-absent, gate: false, params: { skill: ${template.skill.name}, arms: [baseline] } }`,
      `  - { id: rule-grounded, dimension: safety, kind: learning.rule-grounded, gate: false, params: { arms: [learned] } }`,
      `  - { id: no-leak, dimension: safety, kind: learning.no-leak, gate: false, params: { arms: [learned] } }`,
    );
  }
  return lines;
}

export function caseYaml(sibling: GeneratedSibling, libraryDigest: string): string {
  const { template } = sibling;
  const params = Object.keys(sibling.params)
    .sort()
    .map((key) => {
      const value = sibling.params[key]!;
      return `${key}: ${typeof value === "number" ? value : quote(value)}`;
    })
    .join(", ");
  return [
    "schema_version: openneko.eval.case/v1",
    `id: ${sibling.id}`,
    "version: 1.0.0",
    "family: read",
    "product_path: work",
    `dataset: ${DATASET_ID}`,
    `capability_tags: ${flow(template.capabilityTags)}`,
    `difficulty: ${template.difficulty}`,
    `semantics: ${flow(template.semantics)}`,
    "input:",
    "  scenario: skill-procedure",
    `  template: ${template.id}`,
    `  split: ${sibling.split}`,
    `  discovery: ${!sibling.mention}`,
    `  params: { ${params} }`,
    `  skill: ${template.skill.name}`,
    "  skill_library: ../skills",
    `  skill_library_digest: ${quote(libraryDigest)}`,
    `  question: ${quote(questionFor(sibling))}`,
    "oracle:",
    "  kind: sql.metric",
    `  ref: ../oracles/${sibling.id}.sql`,
    "assertions:",
    ...assertionLines(sibling),
    "",
  ].join("\n");
}

function suiteYaml(input: {
  id: string;
  description: string;
  siblings: readonly GeneratedSibling[];
}): string {
  return [
    "schema_version: openneko.eval.suite/v1",
    `id: ${input.id}`,
    "version: 1.0.0",
    `description: ${quote(input.description)}`,
    "cases:",
    ...input.siblings.map(
      (sibling) => `  - { ref: ../datasets/${DATASET_ID}/cases/${sibling.id}.yaml }`,
    ),
    "",
  ].join("\n");
}

const SPLIT_ORDER = { learn: 0, verify: 1, test: 2, holdout: 3, control: 4 } as const;

export function generatedFiles(templates: readonly LearningTemplate[] = TEMPLATES): GeneratedFile[] {
  const siblings = allSiblings(templates);
  const libraryDigest = skillLibraryDigest(templates);
  const dataset = `evals/datasets/${DATASET_ID}`;
  const ordered = [...siblings].sort(
    (left, right) =>
      SPLIT_ORDER[left.split] - SPLIT_ORDER[right.split] || left.id.localeCompare(right.id),
  );
  const files: GeneratedFile[] = [
    ...templates.flatMap(templateSkills).map((skill) => ({
      path: `${dataset}/skills/${skill.name}/SKILL.md`,
      content: skillMarkdown(skill),
    })),
    ...siblings.map((sibling) => ({
      path: `${dataset}/oracles/${sibling.id}.sql`,
      content: `-- Generated from template ${sibling.template.id}. Do not edit; run pnpm --filter @neko/evals learning:generate.\n${sibling.template.sql(sibling.params)}`,
    })),
    ...siblings.map((sibling) => ({
      path: `${dataset}/cases/${sibling.id}.yaml`,
      content: caseYaml(sibling, libraryDigest),
    })),
    {
      path: `${dataset}/splits.yaml`,
      content: [
        "# Generated by packages/evals/scripts/generate-learning-corpus.ts. The split is fixed by the seed.",
        `seed: ${quote(SPLIT_SEED)}`,
        `skill_library_digest: ${quote(libraryDigest)}`,
        "templates:",
        ...templates.flatMap((template) => [
          `  ${template.id}:`,
          `    skill: ${template.skill.name}`,
          `    decoys: ${flow(template.decoys.map((decoy) => decoy.name))}`,
          `    holdout: ${template.split === "holdout"}`,
          "    siblings:",
          ...siblingsOf(template).map(
            (sibling) =>
              `      ${sibling.id}: { split: ${sibling.split}, mention: ${sibling.mention}, params: ${JSON.stringify(sibling.params)} }`,
          ),
        ]),
        "",
      ].join("\n"),
    },
    {
      path: "evals/suites/openneko-skill-learning-v1.yaml",
      content: suiteYaml({
        id: "openneko-skill-learning-v1",
        description: "Measured learning cohort: test-split siblings, held-out templates and one explicit-mention control per template.",
        siblings: ordered.filter(
          (sibling) => sibling.split === "test" || sibling.split === "holdout" || sibling.split === "control",
        ),
      }),
    },
    {
      path: "evals/suites/openneko-skill-learning-corpus-v1.yaml",
      content: suiteYaml({
        id: "openneko-skill-learning-corpus-v1",
        description: "Every learning sibling in split order. The orchestrator reads the learn split from it.",
        siblings: ordered,
      }),
    },
  ];
  return files;
}

async function listFiles(directory: string): Promise<string[]> {
  const entries = await readdir(directory, { withFileTypes: true }).catch(() => []);
  const nested = await Promise.all(
    entries.map((entry) =>
      entry.isDirectory()
        ? listFiles(join(directory, entry.name))
        : Promise.resolve([join(directory, entry.name)]),
    ),
  );
  return nested.flat();
}

async function main(): Promise<void> {
  const check = process.argv.includes("--check");
  const files = generatedFiles();
  const expected = new Set(files.map((file) => resolve(repoRoot, file.path)));
  const managed = ["cases", "oracles", "skills"].map((name) =>
    join(repoRoot, "evals/datasets", DATASET_ID, name),
  );
  const stale = (await Promise.all(managed.map(listFiles))).flat().filter((path) => !expected.has(path));
  const drift: string[] = [...stale.map((path) => relative(repoRoot, path))];
  for (const file of files) {
    const target = resolve(repoRoot, file.path);
    const current = await readFile(target, "utf8").catch(() => null);
    if (current === file.content) continue;
    drift.push(file.path);
    if (!check) {
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, file.content, "utf8");
    }
  }
  if (check) {
    if (drift.length) {
      console.error(`generated learning corpus is stale:\n${drift.join("\n")}`);
      process.exitCode = 1;
    } else {
      console.log(`learning corpus is current: ${files.length} files`);
    }
    return;
  }
  await Promise.all(stale.map((path) => rm(path)));
  console.log(`wrote ${drift.length - stale.length} of ${files.length} files; removed ${stale.length} stale files`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
