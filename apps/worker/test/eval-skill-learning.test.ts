import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { loadEval } from "@neko/evals";
import { buildRunEvidence } from "@neko/llm/work";
import {
  isolatedOrgCohort,
  observeLearnedContext,
  remapSourceNotes,
  type SkillStagingObservation,
} from "../scripts/eval-openneko-backend";
import {
  armInstallPlan,
  collectLearnTargets,
  learningGate,
  runLearnStage,
  stubSkillLearnLlm,
  summarizeCitations,
  verifyLearnedDelivery,
  type LearningArtifact,
  type LearningOutput,
  type TargetLearningRecord,
} from "../scripts/eval-skill-learning";

const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms));

describe("skill-learning concurrency", () => {
  it("holds learned-arm slots until every learn run and the learner finish", async () => {
    const log: string[] = [];
    const active = { now: 0, peak: 0 };
    let starts = 0;
    const learning = learningGate(async () => {
      starts += 1;
      await runLearnStage({
        jobs: [1, 2, 3, 4, 5],
        concurrency: 2,
        run: async (job) => {
          active.now += 1;
          active.peak = Math.max(active.peak, active.now);
          await sleep(((job * 3) % 7) + 1);
          active.now -= 1;
          log.push(`run ${job}`);
          return job * 10;
        },
        learn: async (results) => {
          expect(results).toEqual([10, 20, 30, 40, 50]);
          log.push("learner start");
          await sleep(5);
          log.push("learner end");
        },
      });
    });
    const learnedSlot = async (id: string) => {
      await learning();
      log.push(`slot ${id}`);
    };

    await Promise.all([learning(), learnedSlot("a"), learnedSlot("b"), learnedSlot("c")]);

    expect(starts).toBe(1);
    expect(active.peak).toBe(2);
    const learnerStart = log.indexOf("learner start");
    const learnerEnd = log.indexOf("learner end");
    expect(log.slice(0, learnerStart).filter((entry) => entry.startsWith("run "))).toHaveLength(5);
    expect(log.slice(learnerEnd + 1).sort()).toEqual(["slot a", "slot b", "slot c"]);
  });

  it("runs no learner when a learn run fails", async () => {
    let learned = false;
    await expect(
      runLearnStage({
        jobs: [1, 2, 3],
        concurrency: 3,
        run: async (job) => {
          if (job === 2) throw new Error("learn run failed");
          return job;
        },
        learn: async () => {
          learned = true;
        },
      }),
    ).rejects.toThrow("learn run failed");
    expect(learned).toBe(false);
  });

  it("allows concurrency only for cohorts that isolate one org per episode", async () => {
    const config = (name: string) => resolve(process.cwd(), `../../evals/configs/${name}.yaml`);
    for (const name of ["openneko-skill-learning-scripted", "openneko-implicit-scripted"]) {
      expect(isolatedOrgCohort((await loadEval(config(name))).cases)).toBe(true);
    }
    expect(isolatedOrgCohort((await loadEval(config("openneko-backend-scripted-good-v4"))).cases)).toBe(false);
  });
});

function targetRecord(kind: TargetLearningRecord["kind"], id: string, placebo: boolean): TargetLearningRecord {
  const file = (root: string) => ({ path: `${root}/${kind}/${id}`, digest: `sha256:${root}-${kind}`, bytes: 10 });
  return {
    kind,
    id,
    decision: "applied",
    reason: "applied",
    eventId: null,
    rules: [],
    dropped: [],
    grounding: [],
    leaks: [],
    artifact: file("learned"),
    rulesFile: null,
    placebo: placebo ? file("placebo") : null,
  };
}

const ARTIFACT: Pick<LearningArtifact, "targets"> = {
  targets: {
    "aw-x": targetRecord("skill", "aw-x", true),
    "source:src-learn": targetRecord("source", "src-learn", true),
    "skill-description:aw-x": targetRecord("skill-description", "aw-x", false),
    "org:org": targetRecord("org", "org", true),
  },
};

describe("skill-learning targets", () => {
  it("collects the union of targets every learn run feeds, in learner order", async () => {
    const feeds: Record<string, Array<{ kind: TargetLearningRecord["kind"]; id: string }>> = {
      r1: [{ kind: "skill", id: "aw-b" }, { kind: "source", id: "s1" }, { kind: "org", id: "org" }],
      r2: [{ kind: "skill", id: "aw-a" }, { kind: "source", id: "s1" }, { kind: "org", id: "org" }, { kind: "skill-description", id: "aw-a" }],
      r3: [],
    };
    const targets = await collectLearnTargets(["r1", "r2", "r3"], async (runId) => ({ targets: feeds[runId]! }));
    expect(targets).toEqual([
      { kind: "skill", id: "aw-a" },
      { kind: "skill", id: "aw-b" },
      { kind: "source", id: "s1" },
      { kind: "skill-description", id: "aw-a" },
      { kind: "org", id: "org" },
    ]);
  });

  it("moves source notes onto the test org's source id", () => {
    const raw = "---\ntarget_kind: source\ntarget_id: src-learn\nstatus: applied\n---\n\n- [source:src-learn:L1] Anchor on the latest date.\n- [source:src-learn:L2] Count both endpoints.\n";
    const remapped = remapSourceNotes(raw, "src-test");
    expect(remapped).toContain("target_id: src-test\n");
    expect(remapped).toContain("- [source:src-test:L1] Anchor");
    expect(remapped).toContain("- [source:src-test:L2] Count");
    expect(remapped).not.toContain("src-learn");
  });

  it("gives the learned arm every kind and the placebo generic text for every kind but descriptions", () => {
    expect(armInstallPlan(ARTIFACT, "baseline")).toEqual({ skill_overlays: {}, skill_descriptions: {}, learned_notes: {} });
    expect(armInstallPlan(ARTIFACT, "learned")).toEqual({
      skill_overlays: { "aw-x": "learned/skill/aw-x" },
      skill_descriptions: { "aw-x": "learned/skill-description/aw-x" },
      learned_notes: { source: "learned/source/src-learn", org: "learned/org/org" },
    });
    expect(armInstallPlan(ARTIFACT, "placebo")).toEqual({
      skill_overlays: { "aw-x": "placebo/skill/aw-x" },
      skill_descriptions: {},
      learned_notes: { source: "placebo/source/src-learn", org: "placebo/org/org" },
    });
  });

  it("writes grounded stub output for every target kind", async () => {
    const evidence = ["run-a", "run-b", "run-c"].map((runId) =>
      buildRunEvidence({
        runId,
        status: "completed",
        request: "What share of orders were online?",
        answer: "Current value: 1\nCurrent window: then",
        events: [
          { type: "tool_start", id: "1", name: "read_file", input: { path: "/tmp/skills/aw-x/SKILL.md" } },
          { type: "tool_end", id: "1", result: {} },
          { type: "tool_start", id: "2", name: "mcp_neko_graphjin_execute_graphql", input: { query: "{}" } },
          { type: "tool_end", id: "2", result: {} },
        ],
      }),
    );
    const digests = evidence.map((run) => run.digest).join("\n\n");
    const grounded = (raw: string) => {
      const parsed = JSON.parse(raw) as { operations?: Array<{ evidenceQuotes: string[] }>; evidenceQuotes?: string[] };
      const quotes = parsed.operations?.[0]?.evidenceQuotes ?? parsed.evidenceQuotes ?? [];
      return quotes.length > 0 && evidence.every((run) => quotes.some((quote) => run.digest.includes(quote)));
    };
    for (const header of ["skill: aw-x\nskill guide:", "data source: Frozen eval", "organization: org"]) {
      expect(grounded(await stubSkillLearnLlm(`${header}\nsettled runs:\n${digests}`))).toBe(true);
    }
    const raw = await stubSkillLearnLlm(
      `skill: aw-x\ncurrent description: "Report the share of online orders."\nsettled runs:\n${digests}`,
    );
    expect((JSON.parse(raw) as { description: string }).description).toMatch(/^Report the share of online orders; /u);
    expect(grounded(raw)).toBe(true);
  });
});

describe("skill-learning delivery", () => {
  const staging: SkillStagingObservation = {
    skill: "aw-x",
    runtime: "in-process",
    overlayDigest: "sha256:overlay",
    stagedDigest: "sha256:composed",
    composedDigest: "sha256:composed",
    hasLearnedSection: true,
    readDigests: [],
    descriptionDigest: "sha256:description",
    baseDescriptionDigest: "sha256:base",
    stagedDescriptionDigest: "sha256:description",
  };
  const learned: LearningOutput = {
    arm: "learned",
    overlays: { "aw-x": "sha256:overlay" },
    descriptions: { "aw-x": "sha256:description" },
    sourceNotes: "sha256:notes",
    orgPlaybook: "sha256:playbook",
    rulesLearned: 4,
    ungroundedRules: [],
    leaks: [],
  };
  const prompt = "<learned_data_notes>\n- [source:s2:L1] Anchor.\n</learned_data_notes>\n<org_playbook>\n- [org:org:L1] State the window.\n</org_playbook>";
  const context = observeLearnedContext(
    prompt,
    {
      descriptions: {},
      source: { digest: "sha256:notes", body: "- [source:s2:L1] Anchor.", sourceId: "s2" },
      org: { digest: "sha256:playbook", body: "- [org:org:L1] State the window." },
    },
    ["org:org:L1"],
  );

  it("passes each kind only when the run received it", () => {
    expect(verifyLearnedDelivery({ learning: learned, staging, context })).toEqual([
      { kind: "skill", passed: true, diagnostic: "ok" },
      { kind: "skill-description", passed: true, diagnostic: "ok" },
      { kind: "source", passed: true, diagnostic: "ok" },
      { kind: "org", passed: true, diagnostic: "ok" },
    ]);
    const missing = observeLearnedContext(
      "<org_playbook>\n- other\n</org_playbook>",
      { descriptions: {}, source: { digest: "sha256:notes", body: "- [source:s2:L1] Anchor.", sourceId: "s2" }, org: { digest: "sha256:other", body: "- x" } },
      [],
    );
    expect(
      verifyLearnedDelivery({
        learning: learned,
        staging: { ...staging, stagedDescriptionDigest: "sha256:base", stagedDigest: "sha256:stale" },
        context: missing,
      }).map((entry) => `${entry.kind}:${entry.diagnostic}`),
    ).toEqual([
      "skill:staged_learned_section_differs",
      "skill-description:description_not_staged",
      "source:notes_not_in_prompt",
      "org:installed_notes_differ_from_arm_artifact",
    ]);
    expect(
      verifyLearnedDelivery({ learning: learned, staging }).filter((entry) => !entry.passed).map((entry) => entry.diagnostic),
    ).toEqual(["no_prompt_observation", "no_prompt_observation"]);
  });

  it("keeps the base description for the placebo and nothing learned for the baseline", () => {
    const placebo: LearningOutput = { ...learned, arm: "placebo", descriptions: {} };
    expect(
      verifyLearnedDelivery({ learning: placebo, staging: { ...staging, descriptionDigest: null }, context }).find(
        (entry) => entry.kind === "skill-description",
      ),
    ).toEqual({ kind: "skill-description", passed: false, diagnostic: "staged_description_differs_from_base" });
    const baseline: LearningOutput = { ...learned, arm: "baseline" };
    expect(
      verifyLearnedDelivery({
        learning: baseline,
        staging: { ...staging, hasLearnedSection: false, stagedDescriptionDigest: "sha256:base" },
        context: observeLearnedContext("plain prompt", { descriptions: {}, source: null, org: null }, []),
      }).every((entry) => entry.passed),
    ).toBe(true);
  });

  it("compares citing with non-citing runs per rule", () => {
    expect(
      summarizeCitations([
        { passed: true, cited: ["org:org:L1", "skill:aw-x:L1"] },
        { passed: false, cited: ["org:org:L1"] },
        { passed: true, cited: [] },
      ]),
    ).toEqual({
      rules: { "org:org:L1": { runs: 2, passed: 1 }, "skill:aw-x:L1": { runs: 1, passed: 1 } },
      citing: { runs: 2, passed: 1 },
      nonCiting: { runs: 1, passed: 1 },
    });
  });
});
