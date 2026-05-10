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

const { mockBackendRun, mockResolveBackend, mockResolveBinary, mockEnsureGuard, mockEnsureWorkspace, mockFormatMemoryContext, mockRunAutoMemory } = vi.hoisted(() => ({
  mockBackendRun: vi.fn(),
  mockResolveBackend: vi.fn(),
  mockResolveBinary: vi.fn(),
  mockEnsureGuard: vi.fn(),
  mockEnsureWorkspace: vi.fn(),
  mockFormatMemoryContext: vi.fn(),
  mockRunAutoMemory: vi.fn(),
}));

vi.mock("@neko/llm", async () => {
  const actual = await vi.importActual<typeof import("@neko/llm")>("@neko/llm");
  return {
    ...actual,
    resolveAgentBackend: mockResolveBackend,
  };
});

vi.mock("@neko/llm/work", async () => {
  const actual = await vi.importActual<typeof import("@neko/llm/work")>(
    "@neko/llm/work",
  );
  return {
    ...actual,
    resolveBinaryOnPath: mockResolveBinary,
    ensureGraphjinGuard: mockEnsureGuard,
    ensureWorkWorkspace: mockEnsureWorkspace,
    formatWorkMemoryPromptContext: mockFormatMemoryContext,
    runWorkAutoMemoryPipeline: mockRunAutoMemory,
  };
});

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

describeIfDb("runWorkRun", () => {
  let orgId: string;
  let runWorkRun: typeof import("../../src/jobs/work-run").runWorkRun;

  beforeAll(async () => {
    const mod = await import("../../src/jobs/work-run.js");
    runWorkRun = mod.runWorkRun;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("job-work-run");
    await createTestOrg(orgId);
    mockResolveBackend.mockResolvedValue({
      id: "hermes",
      run: mockBackendRun,
    });
    mockResolveBinary.mockResolvedValue("/usr/local/bin/graphjin");
    mockEnsureGuard.mockResolvedValue(undefined);
    mockEnsureWorkspace.mockResolvedValue(FAKE_WORKSPACE);
    mockFormatMemoryContext.mockResolvedValue("");
    mockRunAutoMemory.mockResolvedValue(undefined);
    mockBackendRun.mockReset();
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
      onEvent: (e: { type: string; role?: string; content?: string }) => Promise<void>;
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

    await runWorkRun("processing-job-id", orgId, {
      runId: run.id,
      threadId: thread.id,
      message: "What's the revenue?",
    });

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
      .select({
        status: work_run.status,
        error: work_run.error,
      })
      .from(work_run)
      .where(eq(work_run.id, run.id))
      .limit(1);
    expect(final[0]?.status).toBe("completed");
    expect(final[0]?.error).toBeNull();

    expect(mockRunAutoMemory).toHaveBeenCalledTimes(1);
  });

  it("backend throws → work_run.status='failed' with the error message; error+done events written", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });

    mockBackendRun.mockRejectedValue(new Error("Hermes spawn failed"));

    await expect(
      runWorkRun("processing-job-id", orgId, {
        runId: run.id,
        threadId: thread.id,
        message: "x",
      }),
    ).rejects.toThrow(/Hermes spawn failed/);

    const events = await db()
      .select({ kind: work_run_event.kind, payload: work_run_event.payload })
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

    expect(mockRunAutoMemory).not.toHaveBeenCalled();
  });

  it("missing thread → throws and never invokes the backend", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });
    await db().delete(work_thread).where(eq(work_thread.id, thread.id));

    await expect(
      runWorkRun("processing-job-id", orgId, {
        runId: run.id,
        threadId: thread.id,
        message: "x",
      }),
    ).rejects.toThrow(/thread .* not found/);
    expect(mockBackendRun).not.toHaveBeenCalled();
  });

  it("graphjin binary missing → fails fast with a clear error", async () => {
    const thread = await insertWorkThread(orgId);
    const run = await insertWorkRun({ orgId, threadId: thread.id });
    mockResolveBinary.mockResolvedValue(null);

    await expect(
      runWorkRun("processing-job-id", orgId, {
        runId: run.id,
        threadId: thread.id,
        message: "x",
      }),
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

    await runWorkRun("processing-job-id", orgId, {
      runId: run.id,
      threadId: thread.id,
      message: "x",
    });

    const seqs = (
      await db()
        .select({ seq: work_run_event.seq })
        .from(work_run_event)
        .where(eq(work_run_event.run_id, run.id))
        .orderBy(asc(work_run_event.seq))
    ).map((r) => r.seq);
    expect(seqs).toEqual([1, 2, 3, 4, 5]);
  });
});
