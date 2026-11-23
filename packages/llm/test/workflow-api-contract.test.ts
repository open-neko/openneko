import { describe, expect, it } from "vitest";
import {
  compileWorkflowApiBatchContract,
  defaultWorkflowApiLimits,
  issueWorkflowApiToken,
  parseCompiledWorkflowBatchContract,
  verifyWorkflowApiTokenDigest,
  workflowApiLimitPatch,
} from "../src/workflows/api-contract";

const compiledBatch = {
  version: 1,
  compiled: true,
  compiler: "workflow",
  recordsField: "records",
  columns: [
    { name: "Account", path: "account.name" },
    { name: "Active", path: "active", default: false },
  ],
};

describe("workflow API contract", () => {
  it("issues a one-time high-entropy token and verifies only its digest", () => {
    const issued = issueWorkflowApiToken();

    expect(issued.token).toMatch(/^onk_wf_[0-9a-f]{12}_[A-Za-z0-9_-]{43}$/);
    expect(issued.prefix).toMatch(/^onk_wf_[0-9a-f]{12}$/);
    expect(issued.verifier).toMatch(/^[0-9a-f]{64}$/);
    expect(verifyWorkflowApiTokenDigest(issued.token, issued.verifier)).toBe(true);
    expect(verifyWorkflowApiTokenDigest(`${issued.token}x`, issued.verifier)).toBe(false);
    expect(verifyWorkflowApiTokenDigest(issued.token, null)).toBe(false);
    expect(verifyWorkflowApiTokenDigest(issued.token, "malformed")).toBe(false);
  });

  it("fails batch mode closed until a safe compiled contract exists", () => {
    expect(
      parseCompiledWorkflowBatchContract({
        apiBatch: {
          ...compiledBatch,
          columns: [{ name: "Unsafe", path: "__proto__.polluted" }],
        },
      }),
    ).toBeNull();
  });

  it("compiles batch readiness from the workflow contract without skills", () => {
    const compiled = compileWorkflowApiBatchContract({
      recordsField: "orders",
      columns: [
        { name: "Order", path: "order.id" },
        { name: "Priority", path: "priority", default: "normal" },
      ],
    });

    expect(compiled).toEqual({
      version: 1,
      compiled: true,
      compiler: "workflow",
      recordsField: "orders",
      columns: [
        { name: "Order", path: "order.id" },
        { name: "Priority", path: "priority", default: "normal" },
      ],
    });
    expect(
      parseCompiledWorkflowBatchContract({
        apiBatch: { ...compiled, compiler: "skill" },
      }),
    ).toBeNull();
  });

  it("validates every configurable limit and cross-field batch bounds", () => {
    expect(workflowApiLimitPatch({ queueCap: 7 })).toEqual({ queueCap: 7 });
    expect(workflowApiLimitPatch({ maxToolCalls: 1_024, maxModelCalls: 1_024 })).toEqual({ maxToolCalls: 1_024, maxModelCalls: 1_024 });
    expect(() => workflowApiLimitPatch({ maxToolCalls: 1_025 })).toThrowError(expect.objectContaining({ code: "limit_out_of_range" }));
    expect(() => workflowApiLimitPatch({ mystery: 1 })).toThrowError(
      expect.objectContaining({ code: "unknown_limit" }),
    );
    expect(() =>
      workflowApiLimitPatch({ batchMaxRecords: 10, batchChunkSize: 20 }),
    ).toThrowError(expect.objectContaining({ code: "invalid_batch_limits" }));
    expect(() => workflowApiLimitPatch({ queueCap: 0 })).toThrowError(
      expect.objectContaining({ code: "limit_out_of_range" }),
    );
  });

  it("takes new API access limits from the workflow agent limits", () => {
    const saved = { tools: process.env.OPENNEKO_WORKFLOW_MAX_TOOL_CALLS, timeout: process.env.OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS };
    process.env.OPENNEKO_WORKFLOW_MAX_TOOL_CALLS = "40";
    process.env.OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS = "300000";
    try {
      expect(defaultWorkflowApiLimits()).toMatchObject({ maxToolCalls: 40, maxModelCalls: 81, maxRuntimeSeconds: 300 });
    } finally {
      if (saved.tools === undefined) delete process.env.OPENNEKO_WORKFLOW_MAX_TOOL_CALLS;
      else process.env.OPENNEKO_WORKFLOW_MAX_TOOL_CALLS = saved.tools;
      if (saved.timeout === undefined) delete process.env.OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS;
      else process.env.OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS = saved.timeout;
    }
  });
});
