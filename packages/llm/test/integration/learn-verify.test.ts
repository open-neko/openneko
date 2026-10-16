// Answer checks and sampled recomputation store verdicts in run_verification,
// reach the learner's evidence, and hold a sampled run's settlement.

import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { and, db, eq, pool, run_verification, work_message, work_run, work_run_event } from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import {
  recordAnswerChecks,
  runRecompute,
  scheduleRecompute,
  verifyRunForLearning,
  type VerifyLlm,
} from "../../src/work/learn-verify-store";
import { runSettledAt } from "../../src/work/learn-target-store";
import { loadRunEvidence } from "../../src/work/skill-learn-evidence";
import { createWorkThread } from "../../src/work/store";

vi.setConfig({ testTimeout: 120_000, hookTimeout: 120_000 });

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const ORDERS = {
  isError: false,
  content: [
    {
      type: "text",
      text: JSON.stringify({
        orders: [
          { created_at: "2022-01-04", grand_total: 1_250_000 },
          { created_at: "2025-12-30", grand_total: 1_911_371 },
        ],
      }),
    },
  ],
};

describeIfDb("run verification", () => {
  let home: string;
  const orgs: string[] = [];

  beforeAll(async () => {
    home = await mkdtemp(join(tmpdir(), "neko-verify-home-"));
    vi.stubEnv("HOME", home);
  });

  afterAll(async () => {
    for (const orgId of orgs) await deleteTestOrg(orgId);
    vi.unstubAllEnvs();
    await rm(home, { recursive: true, force: true });
    await pool().end();
  });

  async function newOrg(prefix: string): Promise<string> {
    const orgId = uniqueOrgId(prefix);
    orgs.push(orgId);
    await createTestOrg(orgId);
    return orgId;
  }

  async function makeRun(orgId: string, answer: string, finishedAt = new Date("2026-10-16T10:00:00Z")): Promise<string> {
    const thread = await createWorkThread(orgId, "verify");
    const [run] = await db()
      .insert(work_run)
      .values({
        org_id: orgId,
        thread_id: thread.id,
        backend: "ax",
        status: "completed",
        actor_role: "service",
        created_at: new Date(finishedAt.getTime() - 60_000),
        finished_at: finishedAt,
      })
      .returning({ id: work_run.id });
    const runId = run!.id;
    await db().insert(work_message).values([
      { org_id: orgId, thread_id: thread.id, run_id: runId, role: "user", content: "What was total revenue?", created_at: new Date(finishedAt.getTime() - 60_000) },
      { org_id: orgId, thread_id: thread.id, run_id: runId, role: "assistant", content: answer, created_at: new Date(finishedAt.getTime() - 1000) },
    ]);
    await db().insert(work_run_event).values([
      {
        org_id: orgId,
        thread_id: thread.id,
        run_id: runId,
        kind: "tool_start",
        payload: { type: "tool_start", id: "t1", name: "mcp_graphjin_execute_graphql", input: { query: "{ orders { created_at grand_total } }" } },
      },
      { org_id: orgId, thread_id: thread.id, run_id: runId, kind: "tool_end", payload: { type: "tool_end", id: "t1", result: ORDERS } },
    ]);
    return runId;
  }

  async function settledAt(runId: string, verifyTimeoutMs?: number): Promise<string | null> {
    const [row] = await db()
      .select({ settledAt: runSettledAt(10 * 60 * 1000, verifyTimeoutMs) })
      .from(work_run)
      .where(eq(work_run.id, runId));
    return row?.settledAt ? new Date(row.settledAt).toISOString() : null;
  }

  const stubLlm = (figure: string): VerifyLlm => async () => ({
    quantity: "total",
    answerFigure: figure,
    query: "{ order_lines { sum_line_total } }",
    resultPath: "order_lines.0.sum_line_total",
  });

  it("stores the four answer checks and feeds failures into the evidence", async () => {
    const orgId = await newOrg("verify-checks");
    const runId = await makeRun(orgId, "From 2014-03-03 to 2014-05-31 revenue was 3,161,371.");
    const checks = await recordAnswerChecks(orgId, runId);
    expect(checks.map((check) => [check.id, check.verdict])).toEqual([
      ["ungrounded_number", "pass"],
      ["ungrounded_date", "fail"],
      ["truncated_data", "pass"],
      ["answer_after_failure", "pass"],
    ]);
    const rows = await db().select().from(run_verification).where(eq(run_verification.run_id, runId));
    expect(rows).toHaveLength(4);
    expect(rows.every((row) => row.kind === "check" && row.org_id === orgId)).toBe(true);
    await recordAnswerChecks(orgId, runId);
    expect(await db().select().from(run_verification).where(eq(run_verification.run_id, runId))).toHaveLength(4);

    const [evidence] = await loadRunEvidence(orgId, [runId]);
    expect(evidence?.verifiedWrong).toBe(true);
    expect(evidence?.digest).toContain(
      "check failed: dates 2014-03-03, 2014-05-31 outside the data range 2022-01-04 to 2025-12-30",
    );
  });

  it("runs the checks in memory when none were stored", async () => {
    const orgId = await newOrg("verify-inline");
    const runId = await makeRun(orgId, "Revenue was 9,876,543.");
    const [evidence] = await loadRunEvidence(orgId, [runId]);
    expect(evidence?.verifiedWrong).toBe(true);
    expect(evidence?.digest).toContain("check failed: number 9,876,543 not found in any data result");
    expect(await db().select().from(run_verification).where(eq(run_verification.run_id, runId))).toHaveLength(0);
  });

  it("holds a sampled run's settlement until the recomputation finishes or times out", async () => {
    const orgId = await newOrg("verify-settle");
    const finished = new Date("2026-10-16T10:00:00Z");
    const runId = await makeRun(orgId, "Total revenue was 3,161,371.", finished);
    expect(await settledAt(runId)).toBe(finished.toISOString());

    expect(await scheduleRecompute(orgId, runId, { rate: 0 })).toBe(false);
    expect(await scheduleRecompute(orgId, runId, { rate: 1 })).toBe(true);
    expect(await scheduleRecompute(orgId, runId, { rate: 1 })).toBe(false);
    expect(await settledAt(runId)).toBe("2026-10-16T10:10:00.000Z");
    expect(await settledAt(runId, 60_000)).toBe("2026-10-16T10:01:00.000Z");

    const result = await runRecompute({
      orgId,
      runId,
      llm: stubLlm("3,161,371"),
      execute: async () => ({ data: { order_lines: [{ sum_line_total: 3_402_118 }] } }),
    });
    expect(result).toEqual({ verdict: "disagree", detail: "independent total 3,402,118 vs answer 3,161,371" });
    const [row] = await db()
      .select()
      .from(run_verification)
      .where(and(eq(run_verification.run_id, runId), eq(run_verification.kind, "recompute")));
    expect(row?.verdict).toBe("disagree");
    expect(row?.data).toMatchObject({ value: 3_402_118, answerFigure: "3,161,371" });
    await db()
      .update(run_verification)
      .set({ updated_at: new Date("2026-10-16T10:04:00Z") })
      .where(eq(run_verification.id, row!.id));
    expect(await settledAt(runId)).toBe("2026-10-16T10:04:00.000Z");

    const [evidence] = await loadRunEvidence(orgId, [runId]);
    expect(evidence?.verifiedWrong).toBe(true);
    expect(evidence?.digest).toContain("recompute disagreed: independent total 3,402,118 vs answer 3,161,371");
    expect(await runRecompute({ orgId, runId, llm: stubLlm("3,161,371") })).toBeNull();
  });

  it("settles on a follow-up in the run's own thread only", async () => {
    const orgId = await newOrg("verify-thread");
    const finished = new Date("2026-10-16T10:00:00Z");
    const runId = await makeRun(orgId, "Total revenue was 3,161,371.", finished);
    await db().update(work_run).set({ actor_role: null }).where(eq(work_run.id, runId));
    const other = await createWorkThread(orgId, "other");
    await db().insert(work_message).values({
      org_id: orgId,
      thread_id: other.id,
      role: "user",
      content: "unrelated",
      created_at: new Date("2026-10-16T10:02:00Z"),
    });
    expect(await settledAt(runId)).toBe("2026-10-16T10:10:00.000Z");
  });

  it("skips runs without a figure and stops at the daily verification budget", async () => {
    const orgId = await newOrg("verify-budget");
    const words = await makeRun(orgId, "Revenue looks healthy.");
    expect(await scheduleRecompute(orgId, words, { rate: 1 })).toBe(false);
    vi.stubEnv("OPENNEKO_LEARN_DAILY_VERIFICATIONS", "1");
    try {
      expect(await scheduleRecompute(orgId, await makeRun(orgId, "Revenue was 1,250,000."), { rate: 1 })).toBe(true);
      expect(await scheduleRecompute(orgId, await makeRun(orgId, "Revenue was 1,911,371."), { rate: 1 })).toBe(false);
    } finally {
      vi.unstubAllEnvs();
      vi.stubEnv("HOME", home);
    }
  });

  it("agrees within tolerance, and refuses a mutation as unknown", async () => {
    const orgId = await newOrg("verify-agree");
    const runId = await makeRun(orgId, "Total revenue was 3,161,371.");
    const agreed = await verifyRunForLearning({
      orgId,
      runId,
      llm: stubLlm("3,161,371"),
      execute: async () => ({ data: { order_lines: [{ sum_line_total: 3_170_000 }] } }),
    });
    expect(agreed.recompute?.verdict).toBe("agree");
    expect(agreed.checks).toHaveLength(4);

    const executed: string[] = [];
    const refused = await runRecompute({
      orgId,
      runId,
      force: true,
      llm: async () => ({ quantity: "total", answerFigure: "3,161,371", query: "mutation { delete_orders { id } }", resultPath: "x" }),
      execute: async ({ query }) => {
        executed.push(query);
        return { data: null };
      },
    });
    expect(refused?.verdict).toBe("unknown");
    expect(refused?.detail).toContain("independent query failed");
    expect(executed).toEqual([]);

    const invented = await runRecompute({ orgId, runId, force: true, llm: stubLlm("4,000,000") });
    expect(invented?.verdict).toBe("unknown");
  });
});
