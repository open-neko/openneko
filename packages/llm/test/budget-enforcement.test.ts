import { describe, expect, it, vi } from "vitest";
import { computeDueWorkflows } from "../src/workflows/cron-sweep";
import { handleSubscriptionMatch } from "../src/workflows/match-handler";
import { startOfTodayUtc } from "../src/workflows/store";
import type {
  SubscriptionRecord,
  WorkflowRecord,
} from "../src/workflows/store";
import type { WorkflowOutputMatch } from "../src/workflows/subscription-query";

function fakeWorkflow(
  overrides: Partial<WorkflowRecord> & { dailyRunBudget?: number | null },
): WorkflowRecord {
  return {
    id: overrides.id ?? "wf-1",
    orgId: overrides.orgId ?? "org-1",
    name: overrides.name ?? "test",
    description: "",
    enabled: overrides.enabled ?? true,
    status: "active",
    goal: "",
    systemPromptOverlay: "",
    steps: [],
    cron: overrides.cron ?? "* * * * *",
    cronTimezone: "UTC",
    cronEnabled: true,
    dailyRunBudget: overrides.dailyRunBudget ?? null,
    outputContract: null,
    createdByThreadId: null,
    createdByRunId: null,
    createdAt: new Date(),
    updatedAt: new Date(),
  };
}

function fakeSubscription(): SubscriptionRecord {
  return {
    id: "sub-1",
    orgId: "org-1",
    workflowId: "wf-consumer",
    sourceKind: "workflow_output",
    filter: {},
    enabled: true,
    debounceMs: 0,
    maxConcurrentRuns: 5,
    maxChainDepthOverride: null,
    idempotencyKeyTemplate: null,
    createdAt: new Date(),
    updatedAt: new Date(),
  };
}

function fakeOutput(): WorkflowOutputMatch {
  return {
    id: "out-1",
    org_id: "org-1",
    workflow_run_id: "wfr-parent",
    kind: "finding",
    scope: "x",
    topic: null,
    mood: "watch",
    title: "x",
    created_at: "2026-05-13T12:00:00.000Z",
  };
}

describe("startOfTodayUtc", () => {
  it("clamps to UTC midnight of the given instant", () => {
    const noon = new Date("2026-05-13T12:34:56.000Z");
    expect(startOfTodayUtc(noon).toISOString()).toBe("2026-05-13T00:00:00.000Z");
  });

  it("respects UTC across day boundaries", () => {
    // Just-before-midnight UTC is still the same UTC day.
    expect(
      startOfTodayUtc(new Date("2026-05-13T23:59:59.000Z")).toISOString(),
    ).toBe("2026-05-13T00:00:00.000Z");
    // Just-after rolls forward.
    expect(
      startOfTodayUtc(new Date("2026-05-14T00:00:01.000Z")).toISOString(),
    ).toBe("2026-05-14T00:00:00.000Z");
  });
});

describe("cron sweep — daily_run_budget", () => {
  it("skips workflows that have hit their daily_run_budget", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:59:30Z"),
      windowEnd: new Date("2026-05-13T12:00:30Z"),
      workflows: [fakeWorkflow({ cron: "0 12 * * *", dailyRunBudget: 3 })],
      countWorkflowRunsSince: async () => 3,
    });
    expect(due).toHaveLength(0);
  });

  it("fires when under the budget", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:59:30Z"),
      windowEnd: new Date("2026-05-13T12:00:30Z"),
      workflows: [fakeWorkflow({ cron: "0 12 * * *", dailyRunBudget: 3 })],
      countWorkflowRunsSince: async () => 2,
    });
    expect(due).toHaveLength(1);
  });

  it("treats null daily_run_budget as unlimited", async () => {
    const due = await computeDueWorkflows({
      windowStart: new Date("2026-05-13T11:59:30Z"),
      windowEnd: new Date("2026-05-13T12:00:30Z"),
      workflows: [fakeWorkflow({ cron: "0 12 * * *", dailyRunBudget: null })],
      countWorkflowRunsSince: async () => {
        throw new Error("should not be called when budget is null");
      },
    });
    expect(due).toHaveLength(1);
  });
});

describe("match handler — daily_run_budget", () => {
  it("drops the match when consumer workflow has exhausted its budget", async () => {
    const enqueue = vi.fn();
    const createObservation = vi.fn();
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription(),
      output: fakeOutput(),
      enqueue: enqueue as never,
      createObservation: createObservation as never,
      countSubscriptionsMatchingOutput: async () => 0,
      countWorkflowRunsForSubscription: async () => 0,
      isWorkflowInAncestorChain: async () => false,
      resolveProducingRunChainDepth: async () => 0,
      getWorkflow: async () =>
        fakeWorkflow({ id: "wf-consumer", dailyRunBudget: 5 }),
      countWorkflowRunsSince: async () => 5,
    });
    expect(decision.action).toBe("dropped");
    if (decision.action === "dropped") {
      expect(decision.reason).toMatch(/daily_run_budget/);
    }
    expect(enqueue).not.toHaveBeenCalled();
    expect(createObservation).not.toHaveBeenCalled();
  });

  it("allows the match when consumer is under budget", async () => {
    const enqueue = vi.fn().mockResolvedValue("job-id");
    const createObservation = vi
      .fn()
      .mockResolvedValue({ id: "obs-1", orgId: "org-1" });
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription(),
      output: fakeOutput(),
      enqueue: enqueue as never,
      createObservation: createObservation as never,
      countSubscriptionsMatchingOutput: async () => 0,
      countWorkflowRunsForSubscription: async () => 0,
      isWorkflowInAncestorChain: async () => false,
      resolveProducingRunChainDepth: async () => 0,
      getWorkflow: async () =>
        fakeWorkflow({ id: "wf-consumer", dailyRunBudget: 5 }),
      countWorkflowRunsSince: async () => 2,
    });
    expect(decision.action).toBe("enqueued");
    expect(enqueue).toHaveBeenCalledTimes(1);
  });

  it("skips the budget check entirely when daily_run_budget is null", async () => {
    const enqueue = vi.fn().mockResolvedValue("job-id");
    const createObservation = vi
      .fn()
      .mockResolvedValue({ id: "obs-1", orgId: "org-1" });
    const countRunsSince = vi.fn();
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription(),
      output: fakeOutput(),
      enqueue: enqueue as never,
      createObservation: createObservation as never,
      countSubscriptionsMatchingOutput: async () => 0,
      countWorkflowRunsForSubscription: async () => 0,
      isWorkflowInAncestorChain: async () => false,
      resolveProducingRunChainDepth: async () => 0,
      getWorkflow: async () =>
        fakeWorkflow({ id: "wf-consumer", dailyRunBudget: null }),
      countWorkflowRunsSince: countRunsSince as never,
    });
    expect(decision.action).toBe("enqueued");
    expect(countRunsSince).not.toHaveBeenCalled();
  });
});
