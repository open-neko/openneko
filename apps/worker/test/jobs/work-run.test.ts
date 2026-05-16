// Integration tests for runChatTurn — the shared agent runtime that the
// worker's runWorkRun adapter and the web's API route both delegate to.
//
// We exercise runChatTurn directly with mocked dependencies (DI) so the
// tests don't have to deal with vitest module-mock indirection through
// pnpm self-references. The worker adapter itself is a 25-line wrapper
// around runChatTurn and is implicitly covered by virtue of using the
// same emit/seq closure shape these tests build.

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
  and,
  asc,
  db,
  eq,
  pool,
  work_run,
  work_run_event,
  work_thread,
} from "@neko/db";
import {
  appendWorkRunEvent,
  getWorkRunEvents,
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

// Builds the seq-counter + DB-persist emit shape that both the worker's
// runWorkRun and the web's API route use. Mirrors the production code
// without coupling the test to either.
async function buildEmit(args: {
  orgId: string;
  threadId: string;
  runId: string;
}): Promise<EmitFn> {
  let seq = (await getWorkRunEvents(args.orgId, args.runId)).length;
  return async (event) => {
    seq += 1;
    await appendWorkRunEvent({ ...args, seq, event });
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
      prefetchKnowledgePack: vi.fn(async () => ({ ok: true as const })),
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

  it("happy path: writes events by seq, finalizes work_run completed, emits done", async () => {
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

    const emit = await buildEmit({ orgId, threadId: thread.id, runId: run.id });
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
      .select({ seq: work_run_event.seq, kind: work_run_event.kind })
      .from(work_run_event)
      .where(
        and(
          eq(work_run_event.org_id, orgId),
          eq(work_run_event.run_id, run.id),
        ),
      )
      .orderBy(asc(work_run_event.seq));

    expect(events.map((e) => e.kind)).toEqual([
      "status",
      "status",
      "message",
      "done",
    ]);
    expect(events.map((e) => e.seq)).toEqual([1, 2, 3, 4]);

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

    const emit = await buildEmit({ orgId, threadId: thread.id, runId: run.id });
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
      .orderBy(asc(work_run_event.seq));
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

    const emit = await buildEmit({ orgId, threadId: thread.id, runId: run.id });

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

    const emit = await buildEmit({ orgId, threadId: thread.id, runId: run.id });
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

  it("retry resumption: seq counter seeds from existing events", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    await db().insert(work_run_event).values([
      {
        org_id: orgId,
        thread_id: thread.id,
        run_id: run.id,
        seq: 1,
        kind: "status",
        payload: { type: "status", message: "leftover 1" },
      },
      {
        org_id: orgId,
        thread_id: thread.id,
        run_id: run.id,
        seq: 2,
        kind: "status",
        payload: { type: "status", message: "leftover 2" },
      },
    ]);

    mockBackendRun.mockImplementation(async () => ({
      finalText: "ok",
      status: "completed",
      backendState: {},
    }));

    const emit = await buildEmit({ orgId, threadId: thread.id, runId: run.id });
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

    const seqs = (
      await db()
        .select({ seq: work_run_event.seq })
        .from(work_run_event)
        .where(eq(work_run_event.run_id, run.id))
        .orderBy(asc(work_run_event.seq))
    ).map((r) => r.seq);
    // 2 leftover + 2 status (Starting/Loading) + 1 done = 5
    expect(seqs).toEqual([1, 2, 3, 4, 5]);
  });
});
