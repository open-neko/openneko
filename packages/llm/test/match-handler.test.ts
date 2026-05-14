import { describe, expect, it, vi } from "vitest";
import { handleSubscriptionMatch } from "../src/workflows/match-handler";
import type { SubscriptionRecord } from "../src/workflows/store";
import type { WorkflowOutputMatch } from "../src/workflows/subscription-query";

function fakeSubscription(
  overrides: Partial<SubscriptionRecord> = {},
): SubscriptionRecord {
  return {
    id: overrides.id ?? "sub-1",
    orgId: overrides.orgId ?? "org-1",
    workflowId: overrides.workflowId ?? "wf-1",
    sourceKind: overrides.sourceKind ?? "workflow_output",
    filter: overrides.filter ?? {},
    enabled: overrides.enabled ?? true,
    debounceMs: overrides.debounceMs ?? 0,
    maxConcurrentRuns: overrides.maxConcurrentRuns ?? 5,
    maxChainDepthOverride: overrides.maxChainDepthOverride ?? null,
    idempotencyKeyTemplate: overrides.idempotencyKeyTemplate ?? null,
    createdAt: new Date(),
    updatedAt: new Date(),
  };
}

function fakeOutput(
  overrides: Partial<WorkflowOutputMatch> = {},
): WorkflowOutputMatch {
  return {
    id: overrides.id ?? "out-1",
    org_id: overrides.org_id ?? "org-1",
    workflow_run_id: overrides.workflow_run_id ?? "wfr-parent",
    kind: overrides.kind ?? "finding",
    scope: overrides.scope ?? "apac_churn",
    topic: overrides.topic ?? null,
    mood: overrides.mood ?? "watch",
    title: overrides.title ?? "Churn spike",
    created_at: overrides.created_at ?? "2026-05-13T12:00:00.000Z",
  };
}

describe("handleSubscriptionMatch", () => {
  it("drops on org mismatch", async () => {
    const enqueue = vi.fn();
    const createObservation = vi.fn();
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription({ orgId: "org-A" }),
      output: fakeOutput({ org_id: "org-B" }),
      enqueue,
      createObservation,
    });
    expect(decision.action).toBe("dropped");
    expect(enqueue).not.toHaveBeenCalled();
    expect(createObservation).not.toHaveBeenCalled();
  });

  it("drops when chain depth exceeds the configured max", async () => {
    const enqueue = vi.fn();
    const createObservation = vi.fn();
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription(),
      output: fakeOutput(),
      enqueue,
      createObservation,
      globalMaxChainDepth: 2,
      resolveProducingRunChainDepth: async () => 2,
    });
    expect(decision.action).toBe("dropped");
    if (decision.action === "dropped") {
      expect(decision.reason).toMatch(/chain depth/);
    }
    expect(enqueue).not.toHaveBeenCalled();
  });

  it("enqueues a fire job with idempotency key and lineage refs on happy path", async () => {
    const enqueue = vi.fn().mockResolvedValue("job-id-xyz");
    const createObservation = vi.fn().mockResolvedValue({
      id: "obs-1",
      orgId: "org-1",
    });
    const decision = await handleSubscriptionMatch({
      subscription: fakeSubscription(),
      output: fakeOutput(),
      enqueue: enqueue as never,
      createObservation: createObservation as never,
      countSubscriptionsMatchingOutput: async () => 0,
      countWorkflowRunsForSubscription: async () => 0,
      countWorkflowRunsSince: async () => 0,
      getWorkflow: async () => null,
      isWorkflowInAncestorChain: async () => false,
      resolveProducingRunChainDepth: async () => 0,
      globalMaxChainDepth: 8,
      globalMaxFanoutPerOutput: 32,
    });
    expect(decision.action).toBe("enqueued");
    if (decision.action === "enqueued") {
      expect(decision.observationId).toBe("obs-1");
      expect(decision.jobId).toBe("job-id-xyz");
    }
    expect(createObservation).toHaveBeenCalledTimes(1);
    expect(enqueue).toHaveBeenCalledTimes(1);

    const [, jobData, jobOpts] = enqueue.mock.calls[0];
    expect(jobData.triggeredBySubscriptionId).toBe("sub-1");
    expect(jobData.triggeredByOutputId).toBe("out-1");
    expect(jobData.triggeredByObservationId).toBe("obs-1");
    expect(jobOpts.singletonKey).toBe(
      "sub-1:out-1:2026-05-13T12:00:00.000Z",
    );
    expect(jobOpts.singletonHours).toBe(1);
  });

  it("honors a custom idempotencyKeyTemplate", async () => {
    const enqueue = vi.fn().mockResolvedValue("job-id");
    const createObservation = vi.fn().mockResolvedValue({ id: "obs-1" });
    await handleSubscriptionMatch({
      subscription: fakeSubscription({
        idempotencyKeyTemplate:
          "sub:{subscription_id}:out:{source_record_id}",
      }),
      output: fakeOutput(),
      enqueue: enqueue as never,
      createObservation: createObservation as never,
      countSubscriptionsMatchingOutput: async () => 0,
      countWorkflowRunsForSubscription: async () => 0,
      countWorkflowRunsSince: async () => 0,
      getWorkflow: async () => null,
      isWorkflowInAncestorChain: async () => false,
      resolveProducingRunChainDepth: async () => 0,
    });
    const jobOpts = enqueue.mock.calls[0][2];
    expect(jobOpts.singletonKey).toBe("sub:sub-1:out:out-1");
  });
});
