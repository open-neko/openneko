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
import { NextRequest } from "next/server";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  and,
  db,
  eq,
  pool,
  processing_job,
  work_run,
  work_thread,
} from "@neko/db";

const { mockGetOrgId, mockEnqueue, mockResolveBackend } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
  mockEnqueue: vi.fn(),
  mockResolveBackend: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

vi.mock("@neko/db/jobs", async () => {
  const actual = await vi.importActual<typeof import("@neko/db/jobs")>(
    "@neko/db/jobs",
  );
  return { ...actual, enqueue: mockEnqueue };
});

vi.mock("@neko/llm", async () => {
  const actual = await vi.importActual<typeof import("@neko/llm")>("@neko/llm");
  return { ...actual, resolveAgentBackend: mockResolveBackend };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/work/runs] skipping: Postgres unreachable.");
}

async function callRunsPost(
  POST: typeof import("@/app/api/work/threads/[threadId]/runs/route").POST,
  args: { threadId: string; body?: unknown },
): Promise<{ status: number; body: unknown }> {
  const req = new NextRequest("http://localhost:3000/test", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: args.body !== undefined ? JSON.stringify(args.body) : undefined,
  });
  const res = await POST(req, {
    params: Promise.resolve({ threadId: args.threadId }),
  });
  const text = await res.text();
  let parsed: unknown;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = text;
  }
  return { status: res.status, body: parsed };
}

describeIfDb("/api/work/threads/[threadId]/runs POST", () => {
  let orgId: string;
  let threadId: string;
  let POST: typeof import("@/app/api/work/threads/[threadId]/runs/route").POST;

  beforeAll(async () => {
    const mod = await import("@/app/api/work/threads/[threadId]/runs/route");
    POST = mod.POST;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("api-work-runs");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
    mockEnqueue.mockResolvedValue("queue-id-stub");
    mockResolveBackend.mockResolvedValue({ id: "hermes", run: vi.fn() });

    const ins = await db()
      .insert(work_thread)
      .values({ org_id: orgId, title: "" })
      .returning();
    threadId = ins[0]!.id;
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("creates work_run + processing_job and enqueues WORK_RUN", async () => {
    const res = await callRunsPost(POST, {
      threadId,
      body: { message: "What's the revenue?" },
    });

    expect(res.status).toBe(200);
    const { runId, backend, jobId } = res.body as {
      runId: string;
      backend: string;
      jobId: string;
    };
    expect(runId).toBeTruthy();
    expect(jobId).toBeTruthy();
    expect(backend).toBe("hermes");

    const runs = await db()
      .select({ status: work_run.status, backend: work_run.backend })
      .from(work_run)
      .where(eq(work_run.id, runId))
      .limit(1);
    expect(runs[0]?.status).toBe("queued");
    expect(runs[0]?.backend).toBe("hermes");

    const procs = await db()
      .select({ kind: processing_job.kind, status: processing_job.status })
      .from(processing_job)
      .where(eq(processing_job.id, jobId))
      .limit(1);
    expect(procs[0]?.kind).toBe("work_run");
    expect(procs[0]?.status).toBe("queued");

    expect(mockEnqueue).toHaveBeenCalledWith(
      "work_run",
      expect.objectContaining({
        processingJobId: jobId,
        orgId,
        runId,
        threadId,
        message: "What's the revenue?",
      }),
    );
  });

  it("rejects empty message with 400", async () => {
    const res = await callRunsPost(POST, { threadId, body: { message: "" } });
    expect(res.status).toBe(400);
    expect(mockEnqueue).not.toHaveBeenCalled();
  });

  it("returns 404 when the thread doesn't belong to the org", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const res = await callRunsPost(POST, {
      threadId: fakeId,
      body: { message: "anything" },
    });
    expect(res.status).toBe(404);
    expect(mockEnqueue).not.toHaveBeenCalled();
  });

  it("rolls work_run + processing_job to failed when enqueue throws (atomic-ish)", async () => {
    mockEnqueue.mockRejectedValue(new Error("pg-boss is on fire"));

    const res = await callRunsPost(POST, {
      threadId,
      body: { message: "anything" },
    });
    expect(res.status).toBe(500);
    expect((res.body as { error: string }).error).toMatch(/pg-boss is on fire/);

    const runs = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(
        and(eq(work_run.org_id, orgId), eq(work_run.thread_id, threadId)),
      );
    expect(runs).toHaveLength(1);
    expect(runs[0]?.status).toBe("failed");
    expect(runs[0]?.error).toMatch(/pg-boss is on fire/);

    const procs = await db()
      .select({ status: processing_job.status, error: processing_job.error })
      .from(processing_job)
      .where(eq(processing_job.org_id, orgId));
    expect(procs).toHaveLength(1);
    expect(procs[0]?.status).toBe("failed");
    expect(procs[0]?.error).toMatch(/pg-boss is on fire/);
  });
});
