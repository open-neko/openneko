import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";
import { describe, expect, it } from "vitest";
import {
  ASSERTION_KINDS,
  SemanticRegistrySchema,
  SemanticRequirementsSchema,
  assertionKindErrors,
  computeSemanticCoverage,
  formatCoverageTable,
  isRegisteredAdapter,
  loadEval,
  type SemanticRegistry,
} from "../src";

const evalsRoot = fileURLToPath(new URL("../../../evals/", import.meta.url));

function entry(
  overrides: Partial<SemanticRegistry["entries"][number]> = {},
): SemanticRegistry["entries"][number] {
  return {
    id: "WORK-EXAMPLE",
    category: "work-context",
    description: "Example semantic",
    verification: "example oracle",
    disposition: "eval",
    owners: ["packages/llm/src/work"],
    ...overrides,
  };
}

function registry(
  entries: SemanticRegistry["entries"],
): SemanticRegistry {
  return {
    schema_version: "openneko.eval.semantics/v1",
    generated_from: "test",
    entries,
  };
}

describe("semantic registry schema", () => {
  it("accepts tiers and controls", () => {
    const parsed = SemanticRegistrySchema.parse(
      registry([
        entry({ tiers: ["deterministic", "live"], controls: ["present", "decoy"] }),
      ]),
    );
    expect(parsed.entries[0]!.tiers).toEqual(["deterministic", "live"]);
  });

  it("rejects unknown and repeated tiers or controls", () => {
    expect(() =>
      SemanticRegistrySchema.parse(
        registry([entry({ tiers: ["soak" as "load"] })]),
      ),
    ).toThrow();
    expect(() =>
      SemanticRegistrySchema.parse(
        registry([entry({ controls: ["present", "present"] })]),
      ),
    ).toThrow(/must not repeat/u);
  });

  it("requires a reason and a covering suite for excluded semantics", () => {
    expect(() =>
      SemanticRegistrySchema.parse(
        registry([entry({ disposition: "excluded" })]),
      ),
    ).toThrow(/excluded semantics require excluded_reason/u);
    expect(() =>
      SemanticRegistrySchema.parse(
        registry([
          entry({ disposition: "excluded", excluded_reason: "platform smoke" }),
        ]),
      ),
    ).toThrow(/excluded semantics require covered_by/u);
    const parsed = SemanticRegistrySchema.parse(
      registry([
        entry({
          disposition: "excluded",
          excluded_reason: "Covered by the restart smoke test",
          covered_by: ["apps/worker/test/shutdown.test.ts"],
        }),
      ]),
    );
    expect(parsed.entries[0]!.disposition).toBe("excluded");
  });

  it("rejects exclusion fields on other dispositions", () => {
    expect(() =>
      SemanticRegistrySchema.parse(
        registry([entry({ covered_by: ["apps/worker/test/x.test.ts"] })]),
      ),
    ).toThrow(/only valid for excluded/u);
  });

  it("keeps the checked-in registry and requirements valid", async () => {
    const semantics = SemanticRegistrySchema.parse(
      parse(await readFile(join(evalsRoot, "semantics.yaml"), "utf8")),
    );
    const requirements = SemanticRequirementsSchema.parse(
      parse(await readFile(join(evalsRoot, "semantic-requirements.yaml"), "utf8")),
    );
    const learn = semantics.entries.find((item) => item.id === "WORK-SKILL-LEARN");
    expect(learn).toMatchObject({
      category: "work-context",
      disposition: "eval",
      tiers: ["deterministic", "live", "adversarial"],
      controls: ["present", "absent", "decoy"],
      owners: ["packages/llm/src/work"],
    });
    const known = new Set(semantics.entries.map((item) => item.id));
    expect(Object.keys(requirements.entries).filter((id) => !known.has(id))).toEqual([]);
    for (const item of semantics.entries.filter((item) => item.disposition === "eval")) {
      expect(item.tiers, item.id).toBeDefined();
      expect(item.controls, item.id).toBeDefined();
    }
  });
});

describe("assertion kind registry", () => {
  const evalCase = (kind: string, dimension: "method" | "safety") => ({
    id: "example",
    assertions: [{ id: "check", dimension, kind, gate: true }],
  });

  it("registers the learning kinds for the skill-learning adapter", () => {
    expect(ASSERTION_KINDS["openneko.skill-learning"]).toMatchObject({
      "learning.rule-grounded": ["safety"],
      "learning.no-leak": ["safety"],
      "learning.overlay-applied": ["method"],
      "learning.overlay-absent": ["method"],
      "trace.skill-loaded": ["method"],
    });
    expect(
      assertionKindErrors(
        "openneko.skill-learning",
        evalCase("learning.no-leak", "safety"),
      ),
    ).toEqual([]);
  });

  it("registers the scenario kinds for the work-backend adapter only", () => {
    for (const kind of [
      "state.memory-saved",
      "state.memory-unchanged",
      "trace.memory-used",
      "state.library-cataloged",
      "trace.library-document-retrieved",
      "state.skill-learn-event",
      "state.learned-rules",
      "trace.skill-overlay",
      "state.skill-discovery",
      "trace.learned-skill-loaded",
      "state.learned-skill-cap",
      "state.catalog-distinct",
      "state.skill-archived",
    ]) {
      expect(ASSERTION_KINDS["openneko.work-backend"], kind).toHaveProperty([kind]);
      expect(ASSERTION_KINDS["openneko.skill-learning"], kind).not.toHaveProperty([kind]);
    }
    for (const kind of ["trace.no-skill-loaded", "answer.numeric-mismatch"]) {
      expect(ASSERTION_KINDS["openneko.skill-learning"], kind).toHaveProperty([kind]);
    }
    expect(
      assertionKindErrors("openneko.work-backend", evalCase("trace.skill-overlay", "method")),
    ).toEqual([]);
    expect(
      assertionKindErrors("openneko.work-backend", evalCase("state.learned-skill-cap", "method")),
    ).toEqual([
      "case example assertion check: kind state.learned-skill-cap allows dimension safety, not method",
    ]);
  });

  it("rejects an unregistered kind, a wrong dimension and an unknown adapter", () => {
    expect(
      assertionKindErrors("openneko.lifecycle", evalCase("trace.skill-loaded", "method")),
    ).toEqual([
      "case example assertion check: kind trace.skill-loaded is not registered for adapter openneko.lifecycle",
    ]);
    expect(
      assertionKindErrors(
        "openneko.skill-learning",
        evalCase("learning.overlay-applied", "safety"),
      ),
    ).toEqual([
      "case example assertion check: kind learning.overlay-applied allows dimension method, not safety",
    ]);
    expect(isRegisteredAdapter("fixture")).toBe(false);
    expect(assertionKindErrors("fixture", evalCase("x.y", "method"))).toEqual([
      "adapter fixture has no assertion kind registry",
    ]);
  });

  it("fails to load a case whose kind its adapter does not register", async () => {
    const root = await mkdtemp(join(tmpdir(), "openneko-eval-kinds-"));
    await writeFile(
      join(root, "case.yaml"),
      `schema_version: openneko.eval.case/v1
id: unknown-kind
version: 1.0.0
family: contract
product_path: work
dataset: fixture
capability_tags: [watcher.condition]
difficulty: contract
semantics: [WATCH-CONDITION]
input: { scenario: unknown-kind }
oracle: { kind: inline.expected, params: { expected: { ok: true } } }
assertions:
  - { id: ok, dimension: ground_truth, kind: answer.exact, gate: true }
`,
      "utf8",
    );
    await writeFile(
      join(root, "dataset.yaml"),
      `schema_version: openneko.eval.dataset/v1
id: fixture
version: 1.0.0
license: test-only
capabilities: [watcher.condition]
snapshots: [{ id: v1, seed: 1 }]
connection: {}
anchor_policy: { kind: fixed, value: "2026-01-01" }
`,
      "utf8",
    );
    await writeFile(
      join(root, "suite.yaml"),
      `schema_version: openneko.eval.suite/v1
id: fixture
version: 1.0.0
cases: [{ ref: ./case.yaml }]
gates: { require_safety: false }
`,
      "utf8",
    );
    const configPath = join(root, "config.yaml");
    await writeFile(
      configPath,
      `schema_version: openneko.eval/v1
id: fixture
adapter: openneko.lifecycle
suite: { ref: ./suite.yaml }
datasets: [{ ref: ./dataset.yaml, snapshot: v1 }]
variants:
  - id: deterministic
    backend: ax
    outer_model: { provider: deterministic, model: no-model-calls }
    data_path: none
artifacts:
  check_in: none
  raw_dir: ${join(root, "raw")}
  state_dir: ${join(root, "state")}
  results_dir: ${join(root, "results")}
`,
      "utf8",
    );
    await expect(loadEval(configPath)).rejects.toThrow(
      /kind answer\.exact is not registered for adapter openneko\.lifecycle/u,
    );
  });

  it("registers every kind used by the checked-in configs", async () => {
    const configs = [
      "adventureworks-20q.yaml",
      "openneko-backend-scripted-good-v4.yaml",
      "openneko-implicit-scripted.yaml",
      "openneko-implicit-flash-lite.yaml",
      "openneko-lifecycle-smoke.yaml",
      "openneko-work-memory-smoke.yaml",
    ];
    for (const name of configs) {
      const loaded = await loadEval(join(evalsRoot, "configs", name));
      expect(isRegisteredAdapter(loaded.config.adapter)).toBe(true);
    }
  });
});

describe("semantic coverage", () => {
  const semantics = registry([
    entry({ id: "DATA-DELEGATED", category: "data-calculation" }),
    entry({ id: "DATA-AUTH", category: "data-calculation", disposition: "declared" }),
    entry({ id: "WORK-SKILLS" }),
    entry({ id: "WORK-SKILL-LEARN" }),
    entry({
      id: "OPS-HEALTH",
      category: "operability-recovery",
      disposition: "excluded",
      excluded_reason: "Platform smoke",
      covered_by: ["apps/openneko/internal/health_test.go"],
    }),
  ]);
  const config = (cases: Parameters<typeof computeSemanticCoverage>[1][number]["cases"]) => ({
    id: "config",
    variants: [{ data_path: "graphjin-agent" as const }],
    cases,
  });

  it("counts case citations and data-path variants as coverage", () => {
    const coverage = computeSemanticCoverage(semantics, [
      config([{ id: "skills", semantics: ["WORK-SKILLS"], assertions: [] }]),
    ]);
    expect([...coverage.covered].sort()).toEqual(["DATA-DELEGATED", "WORK-SKILLS"]);
    expect(coverage.missingEval).toEqual(["WORK-SKILL-LEARN"]);
    expect(coverage.unknown).toEqual([]);
  });

  it("reports unknown semantics cited by cases or assertions", () => {
    const coverage = computeSemanticCoverage(semantics, [
      config([
        {
          id: "bad",
          semantics: ["WORK-SKILL-LEARN", "WORK-NOPE"],
          assertions: [
            {
              id: "a",
              dimension: "method",
              kind: "learning.overlay-applied",
              gate: true,
              semantics: ["SEC-NOPE"],
            },
          ],
        },
      ]),
    ]);
    expect(coverage.unknown).toEqual(["config/bad: SEC-NOPE", "config/bad: WORK-NOPE"]);
    expect(coverage.missingEval).toEqual(["WORK-SKILLS"]);
  });

  it("groups coverage by prefix", () => {
    const coverage = computeSemanticCoverage(semantics, [
      config([{ id: "learn", semantics: ["WORK-SKILL-LEARN"], assertions: [] }]),
    ]);
    expect(coverage.prefixes).toEqual([
      { prefix: "DATA", covered: 1, eval: 1, declared: 1, excluded: 0, total: 2 },
      { prefix: "OPS", covered: 0, eval: 0, declared: 0, excluded: 1, total: 1 },
      { prefix: "WORK", covered: 1, eval: 2, declared: 0, excluded: 0, total: 2 },
    ]);
    const table = formatCoverageTable(coverage.prefixes).split("\n");
    expect(table[0]).toMatch(/^prefix\s+covered\s+eval\s+declared\s+excluded\s+total$/u);
    expect(table.at(-1)).toMatch(/^all\s+2\s+3\s+1\s+1\s+5$/u);
  });
});
