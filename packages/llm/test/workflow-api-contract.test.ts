import { describe, expect, it } from "vitest";
import {
  boundedWorkflowApiResult,
  compileWorkflowApiBatchContract,
  defaultWorkflowApiLimits,
  issueWorkflowApiToken,
  parseCompiledWorkflowBatchContract,
  verifyWorkflowApiTokenDigest,
  workflowApiAnswer,
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

  it("returns valid JSON objects inside the exact terminal result ceiling", () => {
    expect(boundedWorkflowApiResult('{"ok":true}', 1_024)).toEqual({ ok: true });
    const bounded = boundedWorkflowApiResult("🙂".repeat(200), 96);
    expect(Buffer.byteLength(JSON.stringify(bounded), "utf8")).toBeLessThanOrEqual(
      96,
    );
    expect(bounded).toMatchObject({ truncated: true });
  });

  it("returns an API run's answer card as a table with numbers and row details", () => {
    const answer = workflowApiAnswer([{
      version: "v1.0",
      createSurface: {
        surfaceId: "answer-1",
        components: [
          { id: "root", component: "Answer", title: "Orders by region", children: ["table", "callout"] },
          {
            id: "table",
            component: "Table",
            columns: [{ key: "c0", label: "Region" }, { key: "c1", label: "Orders" }, { key: "c2", label: "Due by (UTC)" }, { key: "c3", label: "Orders" }, { key: "c4", label: "" }],
            rows: [{ c0: "North", c1: "12,699.5", c2: "21 Aug", c3: "3", c4: "x" }],
            details: { North: { columns: [{ key: "c0", label: "Store" }, { key: "c1", label: "Orders" }], rows: [{ c0: "Store 1", c1: "7" }] } },
          },
          { id: "callout", component: "Callout", text: "North is behind plan." },
        ],
      },
    }]);
    expect(answer).toEqual({
      title: "Orders by region",
      table: {
        columns: [
          { key: "region", label: "Region" },
          { key: "orders", label: "Orders" },
          { key: "due_by_utc", label: "Due by (UTC)" },
          { key: "orders_2", label: "Orders" },
          { key: "column_5", label: "" },
        ],
        rows: [{ region: "North", orders: 12699.5, due_by_utc: "21 Aug", orders_2: 3, column_5: "x" }],
      },
      details: { North: { columns: [{ key: "store", label: "Store" }, { key: "orders", label: "Orders" }], rows: [{ store: "Store 1", orders: 7 }] } },
      callout: "North is behind plan.",
    });
    expect(workflowApiAnswer([])).toBeNull();
    expect(boundedWorkflowApiResult("Done.", 4_096, answer)).toEqual({ text: "Done.", answer, truncated: false });
    expect(boundedWorkflowApiResult("Done.", 64, answer)).toEqual({ text: "Done.", truncated: false });
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
