// Lessons as observations, admin learning cards, the run-signal trigger, and
// the orchestrator-tree lineage: parent and root runs, version pins and
// consumed channel messages.

import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  and,
  data_source,
  db,
  eq,
  observation,
  observation_run,
  pool,
  run_store_version,
  skill_learn_event,
  spend_ledger,
  work_message,
  work_run,
  work_run_event,
} from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { listLearningEvents, MAX_LEARNING_EVENTS_PER_DAY, recordLearningEvent } from "../../src/work/learn-events";
import { listLessons } from "../../src/work/learn-lessons";
import { learnTargetDir, readLearnedNotes, type LearnTarget } from "../../src/work/learn-target";
import { runSettledAt, targetRuns } from "../../src/work/learn-target-store";
import { readPinnedRules, recordRunChannelMessages } from "../../src/work/run-lineage";
import { recordLearnerSpend } from "../../src/work/learn-spend";
import { LEARNING_SUBSCRIPTION, resetRunSignalDedupe } from "../../src/work/run-signal";
import { loadRunEvidence } from "../../src/work/skill-learn-evidence";
import { readRejectedAt, readRulesAt } from "../../src/work/skill-learn-playbook";
import {
  dismissLesson,
  runLearnForTarget,
  runsSettledWith,
  setSkillLearnOrgEnabled,
} from "../../src/work/skill-learn-store";
import { createWorkMessage, createWorkRun, createWorkThread, finishWorkRun } from "../../src/work/store";
import { getOrgAgentRoot } from "../../src/work/workspace";

vi.setConfig({ testTimeout: 180_000, hookTimeout: 180_000 });

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const QUOTE = "joining sales_order_detail inflated totals";

describeIfDb("learning lineage", () => {
  let home: string;
  const orgs: string[] = [];

  beforeAll(async () => {
    home = await mkdtemp(join(tmpdir(), "neko-lineage-home-"));
    vi.stubEnv("HOME", home);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllEnvs();
    vi.stubEnv("HOME", home);
  });

  afterAll(async () => {
    for (const orgId of orgs) await deleteTestOrg(orgId);
    vi.unstubAllEnvs();
    await rm(home, { recursive: true, force: true });
    await pool().end();
  });

  async function newOrg(prefix: string, enabled = true): Promise<{ orgId: string; sourceId: string }> {
    const orgId = uniqueOrgId(prefix);
    orgs.push(orgId);
    await createTestOrg(orgId);
    if (enabled) await setSkillLearnOrgEnabled({ orgId, enabled: true });
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

  async function makeRun(orgId: string, at: Date, opts: { parentRunId?: string; graphql?: boolean } = {}) {
    const thread = await createWorkThread(orgId, "lineage");
    const run = await createWorkRun(orgId, thread.id, "ax", { userId: null, role: "admin" }, undefined, {
      parentRunId: opts.parentRunId ?? null,
    });
    await db()
      .update(work_run)
      .set({ status: "completed", created_at: at, finished_at: new Date(at.getTime() + 1000) })
      .where(eq(work_run.id, run.id));
    await db().insert(work_message).values([
      { org_id: orgId, thread_id: thread.id, run_id: run.id, role: "user", content: "Rank territories by revenue.", created_at: at },
      { org_id: orgId, thread_id: thread.id, run_id: run.id, role: "assistant", content: "Northwest leads.", created_at: new Date(at.getTime() + 500) },
    ]);
    if (opts.graphql !== false) {
      await db().insert(work_run_event).values([
        { org_id: orgId, thread_id: thread.id, run_id: run.id, kind: "tool_start", payload: { type: "tool_start", id: "t1", name: "execute_graphql", input: { query: "sales" } } },
        { org_id: orgId, thread_id: thread.id, run_id: run.id, kind: "tool_end", payload: { type: "tool_end", id: "t1", error: `fan-out from ${QUOTE}` } },
      ]);
    }
    return { runId: run.id, threadId: thread.id, rootRunId: run.root_run_id as string | null };
  }

  const at = (day: number, minute = 0) => new Date(Date.UTC(2026, 9, day, 0, minute));

  it("records each judged lesson with run lineage, and dismissal blocklists and removes the rule", async () => {
    const { orgId, sourceId } = await newOrg("lineage-lessons");
    const target: LearnTarget = { kind: "source", id: sourceId };
    const runIds: string[] = [];
    for (let i = 0; i < 5; i++) runIds.push((await makeRun(orgId, at(1, i))).runId);
    const llm = async () =>
      JSON.stringify({
        rationale: "fan-out",
        operations: [
          { op: "add", text: "Aggregate order headers before joining detail rows.", evidenceRunIds: runIds.slice(0, 2), evidenceQuotes: [QUOTE] },
          { op: "add", text: "Always trust the first total.", evidenceRunIds: runIds.slice(0, 1), evidenceQuotes: [QUOTE] },
        ],
      });
    const result = await runLearnForTarget({ orgId, target, settlementMs: 0, llm, now: at(2), guardIntervalMs: 0 });
    expect(result.decision).toBe("applied");
    expect(result.eventId).toMatch(/^[0-9a-f-]{36}$/);

    const lessons = await listLessons(orgId);
    const promoted = lessons.find((l) => l.detail.verdict === "promoted")!;
    const dropped = lessons.find((l) => l.detail.verdict === "dropped")!;
    expect(promoted).toMatchObject({
      text: "Aggregate order headers before joining detail rows.",
      status: "active",
      detail: { targetKind: "source", targetId: sourceId, ruleId: "L1", version: result.eventId, enteredVersion: result.eventId },
    });
    expect(promoted.runIds.sort()).toEqual(runIds.slice(0, 2).sort());
    expect(promoted.detail.quotes).toEqual([QUOTE]);
    expect(dropped).toMatchObject({ text: "Always trust the first total.", detail: { reason: "grounded in 1 run(s); needs 2", enteredVersion: null } });
    const lineage = await db().select().from(observation_run).where(eq(observation_run.observation_id, promoted.id));
    expect(lineage).toHaveLength(2);

    const dismissed = await dismissLesson({ orgId, lessonId: promoted.id, userId: "admin-1" });
    expect(dismissed).toEqual({ dismissed: true, removedRule: true });
    const dir = learnTargetDir(getOrgAgentRoot(orgId), target);
    expect(await readRulesAt(dir)).toEqual([]);
    expect(await readRejectedAt(dir)).toContain("aggregate order headers before joining detail rows.");
    expect((await readLearnedNotes(getOrgAgentRoot(orgId), target))?.status).toBe("disabled");
    const [after] = await listLessons(orgId, { status: "dismissed" });
    expect(after).toMatchObject({ id: promoted.id, detail: { dismissedBy: "admin-1" } });
    const events = await db()
      .select({ decision: skill_learn_event.decision, reason: skill_learn_event.reason })
      .from(skill_learn_event)
      .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.target_kind, "source")));
    expect(events).toContainEqual({ decision: "rolled_back", reason: "an admin dismissed the lesson" });

    const later = Date.now() + 60 * 60 * 1000;
    runIds.length = 0;
    for (let i = 0; i < 5; i++) runIds.push((await makeRun(orgId, new Date(later + i * 60_000))).runId);
    const again = await runLearnForTarget({ orgId, target, settlementMs: 0, llm, now: new Date(later + 3_600_000), guardIntervalMs: 0 });
    expect(again.decision).toBe("skipped");
    const blocked = (await listLessons(orgId)).find((l) => l.id === promoted.id)!;
    expect(blocked.status).toBe("dismissed");
    expect(blocked.detail.verdict).toBe("dropped");
    expect(blocked.detail.reason).toBe("rule was rolled back earlier");
    expect(blocked.detail.history.map((h) => h.verdict)).toEqual(["promoted", "dropped"]);
  });

  it("caps learning events per day, records each once, and lists them with dismissable rules", async () => {
    const { orgId } = await newOrg("lineage-events", false);
    const now = at(5, 10);
    const record = (key: string, when = now) =>
      recordLearningEvent({ orgId, kind: "rolled_back", key, title: `Rolled back ${key}.`, body: "Runs went wrong.", now: when });
    expect(await record("a")).toMatchObject({ recorded: true });
    expect(await record("a")).toEqual({ recorded: false, reason: "duplicate" });
    for (let i = 1; i < MAX_LEARNING_EVENTS_PER_DAY; i++) expect(await record(`k${i}`)).toMatchObject({ recorded: true });
    expect(await record("over")).toEqual({ recorded: false, reason: "daily_cap" });
    expect(await record("over", at(6, 1))).toMatchObject({ recorded: true });

    const lines = await listLearningEvents(orgId);
    expect(lines).toHaveLength(MAX_LEARNING_EVENTS_PER_DAY + 1);
    expect(lines[0]).toMatchObject({ line: "Rolled back over. Runs went wrong.", lessonId: null });
    const { briefing_card } = await import("@neko/db");
    expect(await db().select().from(briefing_card).where(eq(briefing_card.org_id, orgId))).toEqual([]);
  });

  it("emits run_finished on finish and human_feedback on the next person's message", async () => {
    const { orgId } = await newOrg("lineage-trigger", false);
    resetRunSignalDedupe();
    const deliver = vi.spyOn(LEARNING_SUBSCRIPTION, "deliver");
    const thread = await createWorkThread(orgId, "trigger");
    const run = await createWorkRun(orgId, thread.id, "ax", { userId: null, role: "admin" });
    await finishWorkRun(run.id, "completed", null);
    expect(deliver).toHaveBeenCalledWith({ kind: "run_finished", orgId, runId: run.id });

    const next = await createWorkRun(orgId, thread.id, "ax", { userId: null, role: "admin" });
    await createWorkMessage({ orgId, threadId: thread.id, runId: next.id, role: "user", content: "That total is wrong." });
    await vi.waitFor(() =>
      expect(deliver).toHaveBeenCalledWith({ kind: "human_feedback", orgId, runId: run.id }),
    );
    await finishWorkRun(run.id, "completed", null);
    expect(deliver.mock.calls.filter(([s]) => s.kind === "run_finished")).toHaveLength(1);
  });

  it("links sub-runs to their root and settles them with it", async () => {
    const { orgId } = await newOrg("lineage-tree", false);
    const root = await makeRun(orgId, at(7));
    const child = await makeRun(orgId, at(7, 5), { parentRunId: root.runId });
    const grandchild = await makeRun(orgId, at(7, 6), { parentRunId: child.runId });
    const [childRow] = await db().select().from(work_run).where(eq(work_run.id, child.runId));
    const [grandRow] = await db().select().from(work_run).where(eq(work_run.id, grandchild.runId));
    expect(childRow).toMatchObject({ parent_run_id: root.runId, root_run_id: root.runId });
    expect(grandRow).toMatchObject({ parent_run_id: child.runId, root_run_id: root.runId });

    await db().insert(work_message).values({
      org_id: orgId,
      thread_id: root.threadId,
      role: "user",
      content: "Use net revenue.",
      created_at: at(7, 30),
    });
    const settled = await db()
      .select({ id: work_run.id, settledAt: runSettledAt(60 * 60 * 1000) })
      .from(work_run)
      .where(eq(work_run.org_id, orgId));
    const settledAt = new Map(settled.map((r) => [r.id, r.settledAt ? new Date(r.settledAt).toISOString() : null]));
    expect(settledAt.get(root.runId)).toBe(at(7, 30).toISOString());
    expect(settledAt.get(child.runId)).toBe(at(7, 30).toISOString());
    expect(settledAt.get(grandchild.runId)).toBe(at(7, 30).toISOString());

    expect(await runsSettledWith(orgId, root.runId)).toEqual([root.runId, child.runId, grandchild.runId]);
    expect(await runsSettledWith(orgId, child.runId)).toEqual([child.runId]);
    await db().update(work_run).set({ finished_at: null, status: "running" }).where(eq(work_run.id, root.runId));
    expect(await runsSettledWith(orgId, child.runId)).toEqual([]);
    await expect(
      createWorkRun(orgId, root.threadId, "ax", undefined, undefined, { parentRunId: "00000000-0000-0000-0000-000000000000" }),
    ).rejects.toThrow(/parent run/);
  });

  it("pins store versions per root task, so a sub-run reads its root's version", async () => {
    const { orgId, sourceId } = await newOrg("lineage-pin", false);
    const target: LearnTarget = { kind: "source", id: sourceId };
    const oldRules = [{ id: "L1", text: "Old rule text here.", evidenceRunIds: [], evidenceQuotes: [] }];
    const [v1] = await db()
      .insert(skill_learn_event)
      .values({
        org_id: orgId,
        skill_name: `source:${sourceId}`,
        target_kind: "source",
        target_id: sourceId,
        decision: "applied",
        model_trace: { versionRules: oldRules },
        created_at: at(8),
      })
      .returning({ id: skill_learn_event.id });
    const root = await makeRun(orgId, at(8, 10));
    const [v2] = await db()
      .insert(skill_learn_event)
      .values({
        org_id: orgId,
        skill_name: `source:${sourceId}`,
        target_kind: "source",
        target_id: sourceId,
        decision: "applied",
        model_trace: { versionRules: [] },
        created_at: at(8, 20),
      })
      .returning({ id: skill_learn_event.id });
    const sub = await makeRun(orgId, at(8, 30), { parentRunId: root.runId });
    const fresh = await makeRun(orgId, at(8, 40));

    const pins = await db()
      .select({ runId: run_store_version.run_id, version: run_store_version.version })
      .from(run_store_version)
      .where(eq(run_store_version.org_id, orgId));
    const pinned = new Map(pins.map((p) => [p.runId, p.version]));
    expect(pinned.get(root.runId)).toBe(v1!.id);
    expect(pinned.get(sub.runId)).toBe(v1!.id);
    expect(pinned.get(fresh.runId)).toBe(v2!.id);

    expect(await readPinnedRules(orgId, sub.runId, target)).toEqual(oldRules);
    const runs = await targetRuns({ orgId, target, settlementMs: 0, now: at(9) });
    expect(runs.find((r) => r.runId === sub.runId)).toMatchObject({ pinnedVersion: v1!.id, versionAt: at(8, 10) });
  });

  it("keeps agent claims and private messages from grounding a rule", async () => {
    const { orgId } = await newOrg("lineage-channel", false);
    const { runId } = await makeRun(orgId, at(10), { graphql: false });
    await recordRunChannelMessages(orgId, runId, [
      { channelThread: "#ops", messageRef: "m1", senderKind: "agent", accessScope: "org", content: "Partial refunds are double counted in refunds." },
      { channelThread: "#ops", messageRef: "m2", senderKind: "human", accessScope: "org", content: "Use credit memos for partial refunds." },
      { channelThread: "dm", messageRef: "m3", senderKind: "human", accessScope: "private", content: "The private margin target is secret." },
      { channelThread: "#ops", messageRef: "m1", senderKind: "agent", accessScope: "org", content: "repeat" },
    ]);
    const [evidence] = await loadRunEvidence(orgId, [runId]);
    expect(evidence!.feedback).toEqual(["Use credit memos for partial refunds."]);
    expect(evidence!.claims).toBe(1);
    expect(evidence!.digest).toContain("Partial refunds are double counted");
    expect(evidence!.grounding).not.toContain("Partial refunds are double counted");
    expect(evidence!.digest).not.toContain("private margin");
  });

  it("records a learner model call as system spend with no run, once per call", async () => {
    const { orgId } = await newOrg("lineage-spend", false);
    let usage = [
      { ai: "google-gemini", model: "gemini-3.7-flash", tokens: { promptTokens: 1000, completionTokens: 200, totalTokens: 1200 } },
    ];
    const generator = {
      getUsage: () => usage as never,
      resetUsage: () => {
        usage = [];
      },
    };
    await recordLearnerSpend(orgId, generator, "skill-learn");
    await recordLearnerSpend(orgId, generator, "skill-learn");
    const rows = await db().select().from(spend_ledger).where(eq(spend_ledger.org_id, orgId));
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      source: "system",
      work_run_id: null,
      provider: "google-gemini",
      model: "gemini-3.7-flash",
      tokens: 1200,
    });
    expect(rows[0]!.cost_micros).toBeGreaterThan(0);
  });
});
