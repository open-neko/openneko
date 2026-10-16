import { resolve } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { loadEval, type EvalExecution, type LoadedCase } from "@neko/evals";
import { workSemanticDigest, type WorkSemanticTraceEvent } from "@neko/llm/work";
import { scoreScenarioWorkBackendExecution } from "../scripts/eval-openneko-backend";
import { buildWorkBackendFixtureSpec } from "../scripts/eval-openneko-backend-fixture";
import {
  goodScenarioLearnLlm,
  nearDuplicateSkills,
  parseScenario,
  scenarioDiscoveryLlm,
  scoreScenarioAssertion,
  type ScenarioObservation,
  type ScenarioStepObservation,
} from "../scripts/eval-openneko-scenario";

const ORACLE = {
  anchorDate: "2025-06-29",
  startDate: "2025-04-01",
  expectedValue: 77,
  baselineValue: 201,
  oracleSqlDigest: workSemanticDigest("oracle"),
};

function step(id: string, overrides: Partial<ScenarioStepObservation> = {}): ScenarioStepObservation {
  return {
    id,
    kind: "chat",
    runIds: [`${id}-run`],
    status: "completed",
    finalText: "",
    skillsLoaded: [],
    skillOverlays: {},
    memoryIdsUsed: [],
    libraryDocumentIds: [],
    memoriesAfter: [],
    catalogAfter: [],
    archivedAfter: [],
    ...overrides,
  };
}

function observation(steps: ScenarioStepObservation[], overrides: Partial<ScenarioObservation> = {}): ScenarioObservation {
  return {
    schemaVersion: "openneko.eval.scenario-observation/v1",
    memoriesBefore: [{ id: "seed", text: "Board reviews use calendar quarters.", scope: "global", seeded: true }],
    catalogBefore: [{ name: "aw-vendor-lead-time", description: "Vendor lead times.", learned: false }],
    runSources: {},
    followups: [],
    documents: {},
    steps,
    ...overrides,
  };
}

function score(kind: string, params: Record<string, unknown>, value: ScenarioObservation): boolean {
  return scoreScenarioAssertion({ id: "check", kind, params }, value).passed;
}

const SEED = { id: "seed", text: "Board reviews use calendar quarters.", scope: "global", seeded: true };
const RULE = { id: "m1", text: "Order counts include online orders only.", scope: "global", seeded: false };

describe("scenario assertion kinds", () => {
  it("state.memory-saved needs a changed global memory with the terms", () => {
    const saved = observation([step("rule", { memoriesAfter: [SEED, RULE] })]);
    expect(score("state.memory-saved", { step: "rule", terms: ["online"] }, saved)).toBe(true);
    const threadOnly = observation([step("rule", { memoriesAfter: [SEED, { ...RULE, scope: "thread" }] })]);
    expect(score("state.memory-saved", { step: "rule", terms: ["online"] }, threadOnly)).toBe(false);
  });

  it("state.memory-saved with exact_count rejects a duplicate instead of an update", () => {
    const first = { id: "r1", text: "Revenue means the total due.", scope: "global", seeded: false };
    const updated = observation([
      step("rule", { memoriesAfter: [first] }),
      step("correct", { memoriesAfter: [{ ...first, text: "Revenue means the order subtotal." }] }),
    ]);
    const params = { step: "correct", terms: ["subtotal"], topic: ["revenue"], exact_count: 1 };
    expect(score("state.memory-saved", params, updated)).toBe(true);
    const duplicated = observation([
      step("rule", { memoriesAfter: [first] }),
      step("correct", { memoriesAfter: [first, { id: "r2", text: "Revenue means the order subtotal.", scope: "global", seeded: false }] }),
    ]);
    expect(score("state.memory-saved", params, duplicated)).toBe(false);
  });

  it("state.memory-unchanged fails when a step saves anything", () => {
    expect(score("state.memory-unchanged", { step: "chat" }, observation([step("chat", { memoriesAfter: [SEED] })]))).toBe(true);
    expect(score("state.memory-unchanged", { step: "chat" }, observation([step("chat", { memoriesAfter: [SEED, RULE] })]))).toBe(false);
  });

  it("trace.memory-used needs the matching memory in the step's retrieved ids", () => {
    const used = observation([step("ask", { memoriesAfter: [SEED, RULE], memoryIdsUsed: ["m1"] })]);
    expect(score("trace.memory-used", { step: "ask", terms: ["online"] }, used)).toBe(true);
    const other = observation([step("ask", { memoriesAfter: [SEED, RULE], memoryIdsUsed: ["seed"] })]);
    expect(score("trace.memory-used", { step: "ask", terms: ["online"] }, other)).toBe(false);
    const threadScoped = observation([step("ask", { memoriesAfter: [{ ...RULE, scope: "thread" }], memoryIdsUsed: ["m1"] })]);
    expect(score("trace.memory-used", { step: "ask", terms: ["online"] }, threadScoped)).toBe(false);
  });

  it("state.library-cataloged and trace.library-document-retrieved read the uploaded documents", () => {
    const documents = {
      current: { documentId: "d1", status: "cataloged", conceptIds: ["c1"] },
      stale: { documentId: "d2", status: "failed", conceptIds: [] },
    };
    const value = observation([step("ask", { libraryDocumentIds: ["d2"] })], { documents });
    expect(score("state.library-cataloged", { documents: ["current"] }, value)).toBe(true);
    expect(score("state.library-cataloged", { documents: ["current", "stale"] }, value)).toBe(false);
    expect(score("trace.library-document-retrieved", { step: "ask", document: "stale" }, value)).toBe(true);
    expect(score("trace.library-document-retrieved", { step: "ask", document: "current" }, value)).toBe(false);
  });

  const runIds = ["w1", "w2", "w3", "w4", "w5"];
  const learnStep = (decision: string, quotes: string[], source: "chat" | "workflow" = "workflow") =>
    observation(
      [
        step("learn", {
          kind: "learn",
          learnEvent: {
            skill: "aw-category-rank",
            decision,
            reason: "test",
            runIds,
            rules: decision === "applied" ? [{ id: "L1", text: "Recheck totals.", evidenceRunIds: ["w1", "w2"], evidenceQuotes: quotes }] : [],
            evidence: {
              w1: "run w1: outcome completed\noperator follow-up: That total looks wrong to me.",
              w2: "run w2: outcome completed\noperator follow-up: That total looks wrong to me.",
            },
          },
        }),
      ],
      {
        runSources: Object.fromEntries(runIds.map((id) => [id, source])),
        followups: [{ runId: "w1", text: "That total looks wrong to me." }],
      },
    );

  it("state.skill-learn-event checks the decision and the run source", () => {
    const params = { step: "learn", skill: "aw-category-rank", decisions: ["applied"], run_source: "workflow", min_runs: 5 };
    expect(score("state.skill-learn-event", params, learnStep("applied", ["looks wrong"]))).toBe(true);
    expect(score("state.skill-learn-event", params, learnStep("skipped", []))).toBe(false);
    expect(score("state.skill-learn-event", params, learnStep("applied", ["looks wrong"], "chat"))).toBe(false);
  });

  it("state.learned-rules requires grounding in a follow-up, and absent rejects one", () => {
    const present = { step: "learn", expect: "present", grounded_in: "followup" };
    const absent = { step: "learn", expect: "absent", grounded_in: "followup" };
    expect(score("state.learned-rules", present, learnStep("applied", ["total looks wrong"]))).toBe(true);
    expect(score("state.learned-rules", present, learnStep("applied", ["run w1: outcome"]))).toBe(false);
    expect(score("state.learned-rules", present, learnStep("applied", ["not in any run"]))).toBe(false);
    expect(score("state.learned-rules", absent, learnStep("applied", ["run w1: outcome"]))).toBe(true);
    expect(score("state.learned-rules", absent, learnStep("applied", ["total looks wrong"]))).toBe(false);
  });

  it("trace.skill-overlay compares the staged learned section with the expectation", () => {
    const applied = observation([step("later", { skillsLoaded: ["aw-x"], skillOverlays: { "aw-x": true } })]);
    expect(score("trace.skill-overlay", { step: "later", skill: "aw-x", applied: true }, applied)).toBe(true);
    expect(score("trace.skill-overlay", { step: "later", skill: "aw-x", applied: false }, applied)).toBe(false);
    expect(score("trace.skill-overlay", { step: "later", skill: "aw-y", applied: true }, applied)).toBe(false);
  });

  const learned = { name: "aw-customer-activity", description: "Count distinct customers in a window.", learned: true };
  const discoverStep = (decision: string, catalog = [learned]) =>
    observation([
      step("discover", {
        kind: "discover",
        discovery: { decision, reason: "test", skillName: decision === "skipped" ? null : learned.name, runIds: [], archived: [] },
        catalogAfter: catalog,
      }),
      step("later", { skillsLoaded: [learned.name] }),
    ]);

  it("state.skill-discovery bounds the decision and the new learned skills", () => {
    const params = { step: "discover", decisions: ["created"], min_new: 1, max_new: 1 };
    expect(score("state.skill-discovery", params, discoverStep("created"))).toBe(true);
    expect(score("state.skill-discovery", params, discoverStep("skipped", []))).toBe(false);
    expect(
      score("state.skill-discovery", params, discoverStep("created", [learned, { ...learned, name: "aw-customer-activity-copy" }])),
    ).toBe(false);
  });

  it("trace.learned-skill-loaded needs a skill that discovery made earlier", () => {
    expect(score("trace.learned-skill-loaded", { step: "later" }, discoverStep("created"))).toBe(true);
    expect(score("trace.learned-skill-loaded", { step: "later" }, discoverStep("skipped", []))).toBe(false);
  });

  it("state.learned-skill-cap counts learned skills only", () => {
    const catalog = (count: number) =>
      Array.from({ length: count }, (_, index) => ({ name: `learned-${index}`, description: `topic ${index}`, learned: true }));
    const at = (count: number) => observation([step("discover", { catalogAfter: [...catalog(count), { name: "custom", description: "c", learned: false }] })]);
    expect(score("state.learned-skill-cap", { step: "discover", max: 20 }, at(20))).toBe(true);
    expect(score("state.learned-skill-cap", { step: "discover", max: 20 }, at(21))).toBe(false);
  });

  it("state.catalog-distinct flags a learned copy of another skill", () => {
    const distinct = [learned, { name: "aw-order-line-volume", description: "Count sales order lines booked in a window.", learned: true }];
    expect(score("state.catalog-distinct", { step: "discover" }, observation([step("discover", { catalogAfter: distinct })]))).toBe(true);
    const copy = [learned, { ...learned, name: "aw-customer-activity-copy" }];
    expect(score("state.catalog-distinct", { step: "discover" }, observation([step("discover", { catalogAfter: copy })]))).toBe(false);
    expect(nearDuplicateSkills([{ ...learned, learned: false }, { ...learned, name: "aw-customer-activity-v2", learned: false }])).toEqual([]);
  });

  it("state.skill-archived needs archived names gone and kept names present", () => {
    const params = { step: "discover", archived: ["aw-weekly-returns"], kept: ["aw-board-pack"] };
    const archived = observation([
      step("discover", { archivedAfter: ["aw-weekly-returns"], catalogAfter: [{ name: "aw-board-pack", description: "b", learned: false }] }),
    ]);
    expect(score("state.skill-archived", params, archived)).toBe(true);
    const frozen = observation([
      step("discover", {
        catalogAfter: [
          { name: "aw-weekly-returns", description: "r", learned: true },
          { name: "aw-board-pack", description: "b", learned: false },
        ],
      }),
    ]);
    expect(score("state.skill-archived", params, frozen)).toBe(false);
  });

  it("fails an assertion bound to a missing step", () => {
    expect(scoreScenarioAssertion({ id: "x", kind: "state.memory-unchanged", params: { step: "nope" } }, observation([])).diagnostic).toBe(
      "step_missing=nope",
    );
  });
});

describe("scenario scoring through the work-backend scorer", () => {
  let offTopic: LoadedCase;
  let libraryAbsent: LoadedCase;

  beforeAll(async () => {
    const loaded = await loadEval(resolve(process.cwd(), "../../evals/configs/openneko-implicit-scripted.yaml"));
    offTopic = loaded.cases.find((item) => item.id === "k1-off-topic")!;
    libraryAbsent = loaded.cases.find((item) => item.id === "k3-library-absent")!;
  });

  function graphjinRead(sequence: number, runId: string): WorkSemanticTraceEvent {
    return {
      schemaVersion: "openneko.work.semantic-trace/v1",
      sequence,
      timestamp: "2026-10-16T00:00:00.000Z",
      runId,
      orgId: "eval-org",
      runKind: "work",
      source: "trusted-broker",
      operation: "graphjin.execute",
      status: "ok",
      durationMs: 1,
      requestDigest: workSemanticDigest({ sequence }),
      responseDigest: workSemanticDigest({ ok: true }),
      evidence: { toolName: "execute_graphql", queryDigest: workSemanticDigest("q"), operationType: "query" },
    } as WorkSemanticTraceEvent;
  }

  function skillLoaded(runId: string, skill: string): WorkSemanticTraceEvent {
    return {
      ...graphjinRead(2, runId),
      source: "trusted-host",
      operation: "skill.loaded",
      evidence: { id: skill, contentDigest: workSemanticDigest(skill) },
    } as WorkSemanticTraceEvent;
  }

  function execution(evalCase: LoadedCase, finalText: string, events: WorkSemanticTraceEvent[], extra: Partial<ScenarioObservation> = {}): EvalExecution {
    const spec = buildWorkBackendFixtureSpec({ caseId: evalCase.id, repetition: 1, scenario: "steps" });
    return {
      output: {
        status: "completed",
        finalText,
        fixture: { spec, orgId: "eval-org", targetResourceIds: {}, decoyResourceIds: {}, crossTenantResourceIds: [] },
        scenario: observation([step("ask", { runIds: ["ask-run"], finalText })], extra),
      },
      semanticEvidence: { schemaVersion: "openneko.eval.semantic-evidence/v1", events },
      measurements: {},
    };
  }

  const answer = (value: number | string, comparison: number | string) =>
    [`Current value: ${value}`, `Comparison value: ${comparison}`, `Current window: ${ORACLE.startDate} through ${ORACLE.anchorDate}`].join("\n");

  it("trace.no-skill-loaded passes an off-topic answer and fails a loaded skill", () => {
    const good = scoreScenarioWorkBackendExecution({
      evalCase: offTopic,
      oracle: ORACLE,
      execution: execution(offTopic, answer(77, 201), [graphjinRead(1, "ask-run")]),
      phase: "initial",
    });
    expect(good.verdict).toBe("pass");
    const bad = scoreScenarioWorkBackendExecution({
      evalCase: offTopic,
      oracle: ORACLE,
      execution: execution(offTopic, answer(77, 201), [graphjinRead(1, "ask-run"), skillLoaded("ask-run", "aw-territory-rank")]),
      phase: "initial",
    });
    expect(bad.verdict).toBe("fail");
    expect(bad.checks.find((check) => check.assertionId === "ask-no-skill-loaded")).toMatchObject({
      passed: false,
      diagnostic: "loaded=aw-territory-rank",
    });
  });

  it("ignores evidence from other steps' runs", () => {
    const value = scoreScenarioWorkBackendExecution({
      evalCase: offTopic,
      oracle: ORACLE,
      execution: execution(offTopic, answer(77, 201), [graphjinRead(1, "ask-run"), skillLoaded("other-run", "aw-territory-rank")]),
      phase: "initial",
    });
    expect(value.verdict).toBe("pass");
  });

  it("answer.numeric-mismatch passes an honest miss and fails an invented value", () => {
    const documents = { stale: { documentId: "d2", status: "cataloged", conceptIds: ["c2"] }, decoy: { documentId: "d3", status: "cataloged", conceptIds: ["c3"] } };
    const honest = scoreScenarioWorkBackendExecution({
      evalCase: libraryAbsent,
      oracle: ORACLE,
      execution: execution(libraryAbsent, answer("not found", "not found"), [], { documents }),
      phase: "initial",
    });
    expect(honest.verdict).toBe("pass");
    const invented = scoreScenarioWorkBackendExecution({
      evalCase: libraryAbsent,
      oracle: ORACLE,
      execution: execution(libraryAbsent, answer(77, 201), [], { documents }),
      phase: "initial",
    });
    expect(invented.checks.find((check) => check.assertionId === "ask-no-invented-value")?.passed).toBe(false);
  });

  it("parses every checked-in scenario", async () => {
    const loaded = await loadEval(resolve(process.cwd(), "../../evals/configs/openneko-implicit-scripted.yaml"));
    for (const evalCase of loaded.cases) {
      const parsed = parseScenario(evalCase);
      const ids = new Set(parsed.steps.map((item) => item.id));
      for (const assertion of evalCase.assertions) {
        if (assertion.kind === "state.library-cataloged") continue;
        expect(ids.has(String(assertion.params?.step)), `${evalCase.id}/${assertion.id}`).toBe(true);
      }
    }
  });
});

describe("scenario learner stubs", () => {
  const digest = (runId: string, followUp?: string) =>
    [`run ${runId}: outcome completed, tool errors 0`, "request: q", ...(followUp ? [`operator follow-up: ${followUp}`] : [])].join("\n");

  it("grounds the good learner in corrections when two runs carry one", async () => {
    const prompt = ["skill: aw-x", "settled runs:", digest("r1", "That total looks wrong to me."), "", digest("r2", "That total looks wrong to me.")].join("\n");
    const proposal = JSON.parse(await goodScenarioLearnLlm(prompt));
    expect(proposal.operations[0]).toMatchObject({ evidenceRunIds: ["r1", "r2"], evidenceQuotes: ["That total looks wrong to me."] });
    const praise = ["skill: aw-x", "settled runs:", digest("r1", "Thanks, these match."), "", digest("r2", "Thanks, these match.")].join("\n");
    expect(JSON.parse(await goodScenarioLearnLlm(praise)).operations[0].evidenceQuotes).toEqual(["skills/aw-x/SKILL.md"]);
  });

  it("cites only runs that contain the discovery quote and under-cites when bad", async () => {
    const prompt = [
      "skill catalog:",
      "- aw-customer-activity: Count distinct customers.",
      "settled runs:",
      digest("r1").replace("request: q", "request: distinct customers"),
      "",
      digest("r2").replace("request: q", "request: distinct customers"),
      "",
      digest("r3").replace("request: q", "request: distinct customers"),
      "",
      digest("r4"),
    ].join("\n");
    const hint = { action: "extend", skill_name: "aw-customer-activity", rule: "Count each customer once.", quote: "distinct customers" };
    expect(JSON.parse(await scenarioDiscoveryLlm(hint, true)(prompt)).evidenceRunIds).toEqual(["r1", "r2", "r3"]);
    expect(JSON.parse(await scenarioDiscoveryLlm(hint, false)(prompt)).evidenceRunIds).toEqual(["r1", "r2"]);
    const duplicate = JSON.parse(await scenarioDiscoveryLlm({ ...hint, bad: "duplicate" }, false)(prompt));
    expect(duplicate.skill).toMatchObject({ name: "aw-customer-activity-copy", description: "Count distinct customers." });
  });
});
