// Typed learning targets share the event log, settlement, batch trigger,
// brakes and rollback guards; these tests run the target-keyed queries.

import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  and,
  data_source,
  db,
  eq,
  learned_rule_use,
  pool,
  skill_learn_event,
  skill_usage,
  work_message,
  work_run,
  work_run_event,
} from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { learnTargetDir, readLearnedNotes, writeLearnedNotes, type LearnTarget } from "../../src/work/learn-target";
import {
  lastLearnAttemptAt,
  learningBrake,
  learnTargetsForRun,
  recordLearnedRuleUse,
} from "../../src/work/learn-target-store";
import { loadRunEvidence } from "../../src/work/skill-learn-evidence";
import { readLearnedDescription } from "../../src/work/skill-overlay";
import { readRejectedAt, readRulesAt, writeRulesAt } from "../../src/work/skill-learn-playbook";
import { runLearnForTarget, setSkillLearnOrgEnabled } from "../../src/work/skill-learn-store";
import { createWorkRun, createWorkThread } from "../../src/work/store";
import { getOrgAgentRoot } from "../../src/work/workspace";

vi.setConfig({ testTimeout: 180_000, hookTimeout: 180_000 });

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const QUOTE = "joining sales_order_detail inflated totals";

describeIfDb("typed learning targets", () => {
  let home: string;
  const orgs: string[] = [];

  beforeAll(async () => {
    home = await mkdtemp(join(tmpdir(), "neko-learn-home-"));
    vi.stubEnv("HOME", home);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.stubEnv("HOME", home);
  });

  afterAll(async () => {
    for (const orgId of orgs) await deleteTestOrg(orgId);
    vi.unstubAllEnvs();
    await rm(home, { recursive: true, force: true });
    await pool().end();
  });

  async function newOrg(prefix: string): Promise<{ orgId: string; sourceId: string }> {
    const orgId = uniqueOrgId(prefix);
    orgs.push(orgId);
    await createTestOrg(orgId);
    await setSkillLearnOrgEnabled({ orgId, enabled: true });
    const [source] = await db()
      .insert(data_source)
      .values({
        org_id: orgId,
        kind: "graphjin",
        graphql_url: "http://gj.test/api/v1/graphql",
        name: "default",
        is_default: true,
        created_at: new Date("2026-01-01T00:00:00Z"),
      })
      .returning({ id: data_source.id });
    return { orgId, sourceId: source!.id };
  }

  async function makeRun(
    orgId: string,
    input: {
      at: Date;
      status?: "completed" | "failed";
      answer?: string;
      graphql?: boolean;
      skills?: string[];
      followUp?: string;
      channel?: string;
      threadId?: string;
    },
  ): Promise<{ runId: string; threadId: string }> {
    const threadId = input.threadId ?? (await createWorkThread(orgId, "learn", input.channel ?? "web")).id;
    const run = await createWorkRun(orgId, threadId, "ax", { userId: null, role: "admin" });
    const finished = new Date(input.at.getTime() + 1000);
    await db()
      .update(work_run)
      .set({ status: input.status ?? "completed", created_at: input.at, finished_at: finished })
      .where(eq(work_run.id, run.id));
    await db().insert(work_message).values([
      { org_id: orgId, thread_id: threadId, run_id: run.id, role: "user", content: "Rank territories by revenue.", created_at: input.at },
      {
        org_id: orgId,
        thread_id: threadId,
        run_id: run.id,
        role: "assistant",
        content: input.answer ?? "Northwest leads.",
        created_at: new Date(input.at.getTime() + 500),
      },
      ...(input.followUp
        ? [{ org_id: orgId, thread_id: threadId, run_id: null, role: "user", content: input.followUp, created_at: new Date(input.at.getTime() + 2000) }]
        : []),
    ]);
    if (input.graphql) {
      await db().insert(work_run_event).values([
        { org_id: orgId, thread_id: threadId, run_id: run.id, kind: "tool_start", payload: { type: "tool_start", id: "t1", name: "execute_graphql", input: { query: "sales" } } },
        { org_id: orgId, thread_id: threadId, run_id: run.id, kind: "tool_end", payload: { type: "tool_end", id: "t1", error: `fan-out from ${QUOTE}` } },
      ]);
    }
    for (const name of input.skills ?? []) {
      await db().insert(skill_usage).values({
        org_id: orgId,
        run_id: run.id,
        skill_name: name,
        content_hash: "hash",
        origin: "custom",
        source: "read",
        first_event_id: 1,
      });
    }
    return { runId: run.id, threadId };
  }

  const at = (minute: number) => new Date(Date.UTC(2026, 9, 1, 0, minute));

  it("maps a run to its skill, source, org and routing targets and skips learning threads", async () => {
    const { orgId, sourceId } = await newOrg("learn-map");
    const { runId } = await makeRun(orgId, {
      at: at(0),
      status: "failed",
      graphql: true,
      skills: ["aw-rank", "aw-trend"],
    });
    const { targets, noSkill } = await learnTargetsForRun(orgId, runId);
    expect(noSkill).toBe(false);
    expect(targets).toEqual([
      { kind: "skill", id: "aw-rank" },
      { kind: "skill", id: "aw-trend" },
      { kind: "source", id: sourceId },
      { kind: "org", id: "org" },
      { kind: "skill-description", id: "aw-rank" },
      { kind: "skill-description", id: "aw-trend" },
    ]);

    const learning = await makeRun(orgId, { at: at(1), graphql: true, channel: "learning" });
    expect((await learnTargetsForRun(orgId, learning.runId)).targets).toEqual([]);
    expect(await loadRunEvidence(orgId, [learning.runId])).toEqual([]);
  });

  it("learns source notes keyed by target, then waits for new runs", async () => {
    const { orgId, sourceId } = await newOrg("learn-source");
    const target: LearnTarget = { kind: "source", id: sourceId };
    const runIds: string[] = [];
    for (let i = 0; i < 5; i++) runIds.push((await makeRun(orgId, { at: at(i), graphql: true })).runId);
    await makeRun(orgId, { at: at(9), graphql: false });

    const prompts: string[] = [];
    const llm = async (prompt: string) => {
      prompts.push(prompt);
      return JSON.stringify({
        rationale: "fan-out",
        operations: [
          {
            op: "add",
            text: "Aggregate order headers before joining sales_order_detail rows.",
            evidenceRunIds: runIds.slice(0, 2),
            evidenceQuotes: [QUOTE],
          },
        ],
      });
    };
    const now = new Date(Date.UTC(2026, 9, 2));
    const result = await runLearnForTarget({ orgId, target, settlementMs: 0, llm, now, guardIntervalMs: 0 });
    expect(result.decision).toBe("applied");
    expect(prompts[0]).toContain("data source: default");
    const notes = await readLearnedNotes(getOrgAgentRoot(orgId), target);
    expect(notes?.body).toBe(`- [source:${sourceId}:L1] Aggregate order headers before joining sales_order_detail rows.`);

    const [event] = await db()
      .select()
      .from(skill_learn_event)
      .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.target_kind, "source")));
    expect(event).toMatchObject({ target_id: sourceId, skill_name: `source:${sourceId}`, decision: "applied" });
    expect(event?.cohort_digest).toMatch(/^[0-9a-f]{64}$/);
    expect((event?.run_ids ?? []).sort()).toEqual([...runIds].sort());

    expect(await lastLearnAttemptAt(orgId, target)).toEqual(event?.created_at);
    expect(await lastLearnAttemptAt(orgId, { kind: "org", id: "org" })).toBeNull();
    expect(await learningBrake(orgId, target)({ cohortDigest: event!.cohort_digest! })).toBe("duplicate_cohort");
    expect(await learningBrake(orgId, { kind: "org", id: "org" })({ cohortDigest: event!.cohort_digest! })).toBeNull();

    const again = await runLearnForTarget({ orgId, target, settlementMs: 0, llm, now, guardIntervalMs: 0 });
    expect(again.reason).toBe("below_repeat_threshold");
    expect(prompts).toHaveLength(1);
  });

  it("writes a learned description on the serving skill and logs the attempt on the queued one", async () => {
    const { orgId } = await newOrg("learn-desc-route");
    const skillsRoot = join(getOrgAgentRoot(orgId), "skills");
    const skills: Record<string, [string, string]> = {
      "aw-stock": ["Count units on hand for products of one color.", "Count units on hand for products of one color."],
      "aw-rank": ["Ranks territories.", "Rank sales territories by revenue for a chosen period."],
    };
    for (const [name, [description, guide]] of Object.entries(skills)) {
      await mkdir(join(skillsRoot, name), { recursive: true });
      await writeFile(join(skillsRoot, name, "SKILL.md"), `---\nname: ${name}\ndescription: ${description}\n---\n\n${guide}\n`, "utf8");
    }
    const runIds: string[] = [];
    for (let i = 0; i < 5; i++) {
      runIds.push((await makeRun(orgId, { at: at(i), status: "failed", graphql: true, skills: ["aw-stock"] })).runId);
    }
    const prompts: string[] = [];
    const description = "Rank sales territories by revenue for a chosen period.";
    const result = await runLearnForTarget({
      orgId,
      target: { kind: "skill-description", id: "aw-stock" },
      settlementMs: 0,
      guardIntervalMs: 0,
      now: new Date(Date.UTC(2026, 9, 2)),
      llm: async (prompt) => {
        prompts.push(prompt);
        return JSON.stringify({ skill: "aw-rank", description, evidenceRunIds: runIds.slice(0, 3), evidenceQuotes: [QUOTE] });
      },
    });
    expect(prompts[0]).toContain("## skill: aw-rank");
    expect(result).toMatchObject({ decision: "applied", target: { kind: "skill-description", id: "aw-rank" } });
    expect(await readLearnedDescription(getOrgAgentRoot(orgId), "aw-rank")).toMatchObject({ status: "applied", description });
    expect(await readLearnedDescription(getOrgAgentRoot(orgId), "aw-stock")).toBeNull();

    const events = await db()
      .select()
      .from(skill_learn_event)
      .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.target_kind, "skill-description")));
    const queued = events.find((event) => event.target_id === "aw-stock");
    const changed = events.find((event) => event.target_id === "aw-rank");
    expect(queued).toMatchObject({ decision: "skipped", reason: "described_other_skill" });
    expect(queued?.cohort_digest).toMatch(/^[0-9a-f]{64}$/);
    expect(changed).toMatchObject({ decision: "applied", skill_name: "skill-description:aw-rank", cohort_digest: null });
    expect(await lastLearnAttemptAt(orgId, { kind: "skill-description", id: "aw-stock" })).toEqual(queued?.created_at);
  });

  it("enforces the daily proposer budget and promotion cap per org", async () => {
    const { orgId } = await newOrg("learn-budget");
    await db().insert(skill_learn_event).values([
      { org_id: orgId, skill_name: "a", target_kind: "skill", target_id: "a", cohort_digest: "d1", decision: "skipped", reason: "no_proposal" },
      { org_id: orgId, skill_name: "b", target_kind: "skill", target_id: "b", cohort_digest: "d2", decision: "applied", reason: "applied" },
    ]);
    const brake = learningBrake(orgId, { kind: "source", id: "s" });
    vi.stubEnv("OPENNEKO_LEARN_DAILY_PROPOSALS", "2");
    expect(await brake({ cohortDigest: "new" })).toBe("daily_learning_budget");
    vi.stubEnv("OPENNEKO_LEARN_DAILY_PROPOSALS", "10");
    vi.stubEnv("OPENNEKO_LEARN_DAILY_PROMOTIONS", "1");
    expect(await brake({ cohortDigest: "new" })).toBe("daily_promotion_cap");
    vi.stubEnv("OPENNEKO_LEARN_DAILY_PROMOTIONS", "5");
    expect(await brake({ cohortDigest: "new" })).toBeNull();

    vi.stubEnv("OPENNEKO_LEARN_DAILY_PROPOSALS", "2");
    await makeRun(orgId, { at: at(0) });
    const braked = await runLearnForTarget({
      orgId,
      target: { kind: "org", id: "org" },
      settlementMs: 0,
      minRepeats: 1,
      guardIntervalMs: 0,
      llm: async () => {
        throw new Error("the proposer must not run");
      },
      now: new Date(Date.UTC(2026, 9, 2)),
    });
    expect(braked).toMatchObject({ decision: "skipped", reason: "daily_learning_budget" });
  });

  it("records cited rules and rolls back only the rule whose citing runs go wrong", async () => {
    const { orgId, sourceId } = await newOrg("learn-cite");
    const target: LearnTarget = { kind: "source", id: sourceId };
    const orgRoot = getOrgAgentRoot(orgId);
    const dir = learnTargetDir(orgRoot, target);
    await writeRulesAt(dir, [
      { id: "L1", text: "Join detail rows before you aggregate.", evidenceRunIds: [], evidenceQuotes: [] },
      { id: "L2", text: "Aggregate order headers first.", evidenceRunIds: [], evidenceQuotes: [] },
    ]);
    await writeLearnedNotes(orgRoot, {
      target,
      status: "applied",
      body: `- [source:${sourceId}:L1] Join detail rows before you aggregate.\n- [source:${sourceId}:L2] Aggregate order headers first.`,
    });
    await db().insert(skill_learn_event).values({
      org_id: orgId,
      skill_name: `source:${sourceId}`,
      target_kind: "source",
      target_id: sourceId,
      decision: "applied",
      reason: "applied",
      created_at: at(0),
    });

    const bogus = await makeRun(orgId, {
      at: at(1),
      graphql: true,
      answer: `Done.\napplied learned rule: source:${sourceId}:L9`,
    });
    expect(await recordLearnedRuleUse(orgId, bogus.runId)).toEqual([]);

    for (let i = 0; i < 4; i++) {
      const { runId } = await makeRun(orgId, {
        at: at(10 + i),
        graphql: true,
        answer: `Northwest leads.\napplied learned rule: source:${sourceId}:L1`,
        followUp: "That total is wrong, it double counts orders.",
      });
      expect(await recordLearnedRuleUse(orgId, runId)).toEqual([`source:${sourceId}:L1`]);
    }
    for (let i = 0; i < 4; i++) {
      const { runId } = await makeRun(orgId, {
        at: at(20 + i),
        graphql: true,
        answer: `Northwest leads.\napplied learned rule: source:${sourceId}:L2`,
      });
      await recordLearnedRuleUse(orgId, runId);
    }
    const uses = await db().select().from(learned_rule_use).where(eq(learned_rule_use.org_id, orgId));
    expect(uses).toHaveLength(8);
    expect(uses[0]).toMatchObject({ target_kind: "source", target_id: sourceId });
    const [citeEvent] = await db()
      .select({ payload: work_run_event.payload })
      .from(work_run_event)
      .where(and(eq(work_run_event.org_id, orgId), eq(work_run_event.kind, "learned_rule_use")))
      .limit(1);
    expect(citeEvent?.payload).toMatchObject({ type: "learned_rule_use", ruleKeys: [`source:${sourceId}:L1`] });

    const result = await runLearnForTarget({
      orgId,
      target,
      settlementMs: 0,
      guardIntervalMs: 0,
      now: new Date(Date.UTC(2026, 9, 2)),
      llm: async () => {
        throw new Error("the guard acts before the proposer");
      },
    });
    expect(result.decision).toBe("rolled_back");
    expect(result.reason).toContain(`rule source:${sourceId}:L1`);
    expect((await readRulesAt(dir)).map((rule) => rule.id)).toEqual(["L2"]);
    expect(await readRejectedAt(dir)).toEqual(["join detail rows before you aggregate."]);
    const notes = await readLearnedNotes(orgRoot, target);
    expect(notes?.status).toBe("applied");
    expect(notes?.body).not.toContain("L1]");
    expect(await readFile(join(dir, "NOTES.md"), "utf8")).toContain("Aggregate order headers first.");
  });
});
