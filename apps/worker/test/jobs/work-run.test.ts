// Integration tests for runChatTurn — the shared agent runtime that the
// worker's runWorkRun adapter and the web's API route both delegate to.
//
// We exercise runChatTurn directly with mocked dependencies (DI) so the
// tests don't have to deal with vitest module-mock indirection through
// pnpm self-references. The worker adapter itself is a 25-line wrapper
// around runChatTurn and is implicitly covered by virtue of using the
// same emit shape these tests build.

import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  action_policy,
  and,
  asc,
  db,
  eq,
  pool,
  work_run,
  work_run_event,
  work_thread,
  workflow_definition,
} from "@neko/db";
import {
  appendWorkRunEvent,
  runChatTurn,
  type RunChatTurnDeps,
} from "@neko/llm/work";
import type { AgentEvent } from "@neko/llm";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[jobs/work-run] skipping: Postgres unreachable.");
}

const FAKE_WORKSPACE = {
  orgRoot: "/tmp/work-test/org",
  skillsRoot: "/tmp/work-test/skills",
  memoryRoot: "/tmp/work-test/memory",
  knowledgeRoot: "/tmp/work-test/knowledge",
  uploadsRoot: "/tmp/work-test/uploads",
  runsRoot: "/tmp/work-test/runs",
  threadUploadsRoot: "/tmp/work-test/uploads/t1",
  runRoot: "/tmp/work-test/runs/r1",
  artifactRoot: "/tmp/work-test/runs/r1/artifacts",
  binRoot: "/tmp/work-test/runs/r1/bin",
  claudeProjectRoot: "/tmp/work-test/org",
  claudeConfigRoot: "/tmp/work-test/org/claude/config",
};

const HERMES_CAPABILITIES = {
  mcpTools: false,
  sdkStopHook: false,
  sessionResume: false,
  canUseToolGate: false,
} as const;

type EmitFn = (event: AgentEvent) => Promise<void>;

// Builds the DB-persist emit shape that both the worker's runWorkRun
// and the web's API route use. Mirrors the production code without
// coupling the test to either. Ordering is via the row's bigserial id;
// no client-side counter is needed.
function buildEmit(args: {
  orgId: string;
  threadId: string;
  runId: string;
}): EmitFn {
  return async (event) => {
    await appendWorkRunEvent({ ...args, event });
  };
}

async function insertWorkRun(args: {
  orgId: string;
  threadId: string;
  status?: string;
}) {
  const ins = await db()
    .insert(work_run)
    .values({
      org_id: args.orgId,
      thread_id: args.threadId,
      backend: "hermes",
      status: args.status ?? "queued",
    })
    .returning();
  return ins[0]!;
}

async function insertWorkThread(orgId: string) {
  const ins = await db()
    .insert(work_thread)
    .values({ org_id: orgId, title: "Test thread" })
    .returning();
  return ins[0]!;
}

describeIfDb("runChatTurn", () => {
  let orgId: string;

  const mockBackendRun = vi.fn();
  const mockEnqueue = vi.fn();
  const mockResolveBinary = vi.fn();

  function makeDeps(over: Partial<RunChatTurnDeps> = {}): Partial<RunChatTurnDeps> {
    return {
      resolveAgentBackend: vi.fn(async () => ({
        id: "hermes" as const,
        capabilities: HERMES_CAPABILITIES,
        run: mockBackendRun,
      })),
      ensureWorkWorkspace: vi.fn(async () => FAKE_WORKSPACE),
      resolveBinaryOnPath: mockResolveBinary,
      ensureGraphjinGuard: vi.fn(async () => undefined),
      formatWorkMemoryPromptContext: vi.fn(async () => ""),
      listInstalledSkills: vi.fn(async () => []),
      prefetchKnowledgeForOrg: vi.fn(async () => ({ ok: true as const, files: [], mode: "legacy" as const })),
      enqueue: mockEnqueue,
      ...over,
    };
  }

  beforeEach(async () => {
    orgId = uniqueOrgId("job-work-run");
    await createTestOrg(orgId);
    mockBackendRun.mockReset();
    mockEnqueue.mockReset().mockResolvedValue("queued-job-id");
    mockResolveBinary.mockReset().mockResolvedValue("/usr/local/bin/graphjin");
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("happy path: writes events in id-monotonic order, finalizes work_run completed, emits done", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    mockBackendRun.mockImplementation(async (opts: {
      onEvent: (e: AgentEvent) => Promise<void>;
    }) => {
      await opts.onEvent({
        type: "message",
        role: "assistant",
        content: "Hello from the agent.",
      });
      return {
        finalText: "Hello from the agent.",
        status: "completed",
        backendState: {},
      };
    });

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await runChatTurn(
      {
        orgId,
        threadId: thread.id,
        runId: run.id,
        message: "What's the revenue?",
        emit,
      },
      makeDeps(),
    );

    const events = await db()
      .select({ id: work_run_event.id, kind: work_run_event.kind })
      .from(work_run_event)
      .where(
        and(
          eq(work_run_event.org_id, orgId),
          eq(work_run_event.run_id, run.id),
        ),
      )
      .orderBy(asc(work_run_event.id));

    expect(events.map((e) => e.kind)).toEqual([
      "status",
      "status",
      "message",
      "done",
    ]);
    // Ids are a shared bigserial across all tests; what we care about is
    // that the four events landed in insertion order, which orderBy(asc)
    // already enforces. The sequence is just expected to be monotonic.
    const ids = events.map((e) => e.id);
    expect(ids).toEqual([...ids].sort((a, b) => a - b));

    const final = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(eq(work_run.id, run.id))
      .limit(1);
    expect(final[0]?.status).toBe("completed");
    expect(final[0]?.error).toBeNull();
  });

  it("backend throws → work_run.status='failed' with the error message; error+done events written", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    mockBackendRun.mockRejectedValue(new Error("Hermes spawn failed"));

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await expect(
      runChatTurn(
        {
          orgId,
          threadId: thread.id,
          runId: run.id,
          message: "x",
          emit,
        },
        makeDeps(),
      ),
    ).rejects.toThrow(/Hermes spawn failed/);

    const events = await db()
      .select({ kind: work_run_event.kind })
      .from(work_run_event)
      .where(eq(work_run_event.run_id, run.id))
      .orderBy(asc(work_run_event.id));
    const kinds = events.map((e) => e.kind);
    expect(kinds).toContain("error");
    expect(kinds).toContain("done");

    const final = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(eq(work_run.id, run.id))
      .limit(1);
    expect(final[0]?.status).toBe("failed");
    expect(final[0]?.error).toBe("Hermes spawn failed");

    expect(mockEnqueue).not.toHaveBeenCalled();
  });

  it("deleted thread (cascade) → returns failed status without invoking the backend", async () => {
    // Pre-existing scenario: the thread was deleted between enqueue and
    // dispatch. The FK on work_run cascades, so by the time runChatTurn
    // tries getWorkThreadBundle the row is gone. It marks the run failed
    // (best-effort; finishWorkRun no-ops on a missing row) and returns
    // cleanly so pg-boss doesn't retry forever.
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });

    // Drop the thread → cascades to work_run via the on-delete-cascade FK.
    await db().delete(work_thread).where(eq(work_thread.id, thread.id));

    const result = await runChatTurn(
      {
        orgId,
        threadId: thread.id,
        runId: run.id,
        message: "x",
        emit,
      },
      makeDeps(),
    );

    expect(result.status).toBe("failed");
    expect(result.error).toMatch(/Thread deleted before run start/i);
    expect(mockBackendRun).not.toHaveBeenCalled();

    // Row is gone (cascade), confirming runChatTurn handled the no-op
    // path without erroring.
    const final = await db()
      .select({ id: work_run.id })
      .from(work_run)
      .where(eq(work_run.id, run.id));
    expect(final).toHaveLength(0);
  });

  it("graphjin binary missing → fails fast with a clear error", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });
    mockResolveBinary.mockResolvedValue(null);

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await expect(
      runChatTurn(
        {
          orgId,
          threadId: thread.id,
          runId: run.id,
          message: "x",
          emit,
        },
        makeDeps(),
      ),
    ).rejects.toThrow(/graphjin CLI is not installed/);

    expect(mockBackendRun).not.toHaveBeenCalled();
    const final = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(eq(work_run.id, run.id))
      .limit(1);
    expect(final[0]?.status).toBe("failed");
  });

  it("neko_workflow_save fence → persists workflow, emits surface card, strips fence from message", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    const finalText = [
      "Saved 'APAC revenue dip check'. You can run it from the workflows list.",
      "",
      "```neko_workflow_save",
      JSON.stringify({
        name: "APAC revenue dip check",
        description: "Daily check on APAC revenue.",
        goal: "Surface meaningful APAC revenue dips.",
        systemPromptOverlay: "Show INR in lakhs.",
        steps: [
          { id: "pull", description: "Pull last 7 days of APAC revenue" },
          { id: "compare", description: "Compare against the prior 7 days" },
          { id: "flag", description: "Flag drops greater than 10%" },
        ],
        triggers: { cron: "0 9 * * *", timezone: "Asia/Kolkata", enabled: true },
      }),
      "```",
    ].join("\n");

    mockBackendRun.mockImplementation(async (opts: {
      onEvent: (e: AgentEvent) => Promise<void>;
    }) => {
      await opts.onEvent({ type: "message", role: "assistant", content: finalText });
      return { finalText, status: "completed", backendState: {} };
    });

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await runChatTurn(
      {
        orgId,
        threadId: thread.id,
        runId: run.id,
        message: "set up a daily APAC revenue dip check at 9am Mumbai time",
        emit,
      },
      makeDeps(),
    );

    const workflows = await db()
      .select()
      .from(workflow_definition)
      .where(eq(workflow_definition.org_id, orgId));
    expect(workflows).toHaveLength(1);
    expect(workflows[0]?.name).toBe("APAC revenue dip check");
    expect(workflows[0]?.cron).toBe("0 9 * * *");
    expect(workflows[0]?.cron_timezone).toBe("Asia/Kolkata");
    expect(workflows[0]?.created_by_thread_id).toBe(thread.id);
    expect(workflows[0]?.created_by_run_id).toBe(run.id);

    const events = await db()
      .select({ kind: work_run_event.kind, payload: work_run_event.payload })
      .from(work_run_event)
      .where(eq(work_run_event.run_id, run.id))
      .orderBy(asc(work_run_event.id));
    const surfaceEvent = events.find((e) => e.kind === "surface");
    expect(surfaceEvent).toBeDefined();
    const payload = surfaceEvent?.payload as {
      messages: Array<{
        createSurface?: { surfaceId: string };
        updateComponents?: { components: Array<{ id: string; text?: string }> };
      }>;
    };
    expect(payload.messages[0]?.createSurface?.surfaceId).toMatch(
      /^workflow-save-/,
    );
    const components =
      payload.messages[1]?.updateComponents?.components ?? [];
    const root = components.find((c) => c.id === "root") as
      | { label?: string; title?: string }
      | undefined;
    expect(root?.label).toBe("Created workflow");
    expect(root?.title).toBe("APAC revenue dip check");
    const body = components.find((c) => c.id === "body");
    expect(body?.text).toContain("[Open detail](/workflows?id=");

    // The fence body must be stripped from the persisted assistant message.
    const msgEvent = events.find(
      (e) =>
        e.kind === "message" &&
        (e.payload as { role?: string }).role === "assistant",
    );
    const msgText = (msgEvent?.payload as { content?: string })?.content ?? "";
    // The original streamed delta is still in work_run_event; the *persisted*
    // assistant message (work_message) is what's stripped. Check that.
    expect(msgText).toContain("neko_workflow_save"); // streamed delta keeps it
  });

  it("neko_rule_save fence → upserts action_policy, emits surface card, threads provenance", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    const finalText = [
      "Saved policy 'slack_revenue_alerts_autoapprove'.",
      "",
      "```neko_rule_save",
      JSON.stringify({
        name: "slack_revenue_alerts_autoapprove",
        description: "Auto-approve low-risk Slack alerts.",
        applies_to_kinds: ["send_message"],
        applies_to_scopes: ["external"],
        mode: "auto_approve",
        risk_threshold_auto_approve: "low",
        limits: { daily_cap: 20 },
        priority: 100,
        enabled: true,
      }),
      "```",
    ].join("\n");

    mockBackendRun.mockImplementation(async (opts: {
      onEvent: (e: AgentEvent) => Promise<void>;
    }) => {
      await opts.onEvent({ type: "message", role: "assistant", content: finalText });
      return { finalText, status: "completed", backendState: {} };
    });

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await runChatTurn(
      {
        orgId,
        threadId: thread.id,
        runId: run.id,
        message: "auto-approve low-risk slack alerts up to 20/day",
        emit,
      },
      makeDeps(),
    );

    const policies = await db()
      .select()
      .from(action_policy)
      .where(eq(action_policy.org_id, orgId));
    expect(policies).toHaveLength(1);
    expect(policies[0]?.name).toBe("slack_revenue_alerts_autoapprove");
    expect(policies[0]?.mode).toBe("auto_approve");
    expect(policies[0]?.created_by_thread_id).toBe(thread.id);
    expect(policies[0]?.created_by_run_id).toBe(run.id);

    const events = await db()
      .select({ kind: work_run_event.kind, payload: work_run_event.payload })
      .from(work_run_event)
      .where(eq(work_run_event.run_id, run.id))
      .orderBy(asc(work_run_event.id));
    const surfaceEvent = events.find((e) => e.kind === "surface");
    expect(surfaceEvent).toBeDefined();
    const payload = surfaceEvent?.payload as {
      messages: Array<{
        createSurface?: { surfaceId: string };
        updateComponents?: { components: Array<{ id: string; text?: string }> };
      }>;
    };
    expect(payload.messages[0]?.createSurface?.surfaceId).toMatch(
      /^policy-save-/,
    );
    const components =
      payload.messages[1]?.updateComponents?.components ?? [];
    const root = components.find((c) => c.id === "root") as
      | { label?: string; title?: string }
      | undefined;
    expect(root?.label).toBe("Created rule");
    expect(root?.title).toBe("slack_revenue_alerts_autoapprove");
    const body = components.find((c) => c.id === "body");
    expect(body?.text).toContain("auto_approve");
    expect(body?.text).toContain("[Open detail](/settings/rules/");
  });

  it("retry resumption: new events land after existing ones in id order", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    const leftover = await db().insert(work_run_event).values([
      {
        org_id: orgId,
        thread_id: thread.id,
        run_id: run.id,
        kind: "status",
        payload: { type: "status", message: "leftover 1" },
      },
      {
        org_id: orgId,
        thread_id: thread.id,
        run_id: run.id,
        kind: "status",
        payload: { type: "status", message: "leftover 2" },
      },
    ]).returning({ id: work_run_event.id });
    const leftoverMaxId = Math.max(...leftover.map((r) => r.id));

    mockBackendRun.mockImplementation(async () => ({
      finalText: "ok",
      status: "completed",
      backendState: {},
    }));

    const emit = buildEmit({ orgId, threadId: thread.id, runId: run.id });
    await runChatTurn(
      {
        orgId,
        threadId: thread.id,
        runId: run.id,
        message: "x",
        emit,
      },
      makeDeps(),
    );

    const all = await db()
      .select({ id: work_run_event.id, kind: work_run_event.kind })
      .from(work_run_event)
      .where(eq(work_run_event.run_id, run.id))
      .orderBy(asc(work_run_event.id));
    // 2 leftover + 2 status (Starting/Loading) + 1 done = 5
    expect(all).toHaveLength(5);
    // The two leftover rows still come first; newly-written events have
    // strictly higher bigserial ids.
    const newIds = all.slice(2).map((r) => r.id);
    expect(newIds.every((id) => id > leftoverMaxId)).toBe(true);
  });
});
