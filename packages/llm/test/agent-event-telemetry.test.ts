import { describe, expect, it } from "vitest";
import {
  MemoryObservationSink,
  HarnessRunSummaryAccumulator,
  createHarnessObserver,
} from "@neko/telemetry";
import { createAgentEventTelemetry } from "../src/work/agent-event-telemetry";

describe("agent event telemetry", () => {
  it("records one total Harness cost without adding stage or inner token usage twice", async () => {
    const sink = new MemoryObservationSink();
    const summary = new HarnessRunSummaryAccumulator("cost-run");
    const telemetry = createAgentEventTelemetry({observer:createHarnessObserver({runId:"cost-run",sinks:[sink,summary]}),operationId:"work:cost-run"});
    const cost = {type:"cost" as const, source:"harness" as const, chargedMicros:9200, budgetMicros:20_000, pricingVersion:"operator-2026-09"};
    await telemetry.startAgent({backend:"harness"});
    await telemetry.observeEvent({type:"usage",source:"outer",usage:{totalTokens:50,coverage:"complete"}});
    await telemetry.observeEvent({type:"stage_usage",source:"harness",stage:"executor",requests:1,reported:1,usage:{totalTokens:50,coverage:"complete"}});
    await telemetry.observeEvent(cost);
    await telemetry.finishAgent({status:"ok",cost});
    expect(sink.observations.filter(item=>item.kind==="run.cost")).toHaveLength(1);
    expect(summary.snapshot().usage).toMatchObject({totalTokens:50,estimatedCostUsd:0.0092,
      pricingCatalogVersion:"operator-2026-09",costStatus:"estimated"});
  });
  it("exports Harness stage attribution without adding it to run usage", async () => {
    const sink = new MemoryObservationSink();
    const summary = new HarnessRunSummaryAccumulator("stage-run");
    const telemetry = createAgentEventTelemetry({ observer: createHarnessObserver({ runId: "stage-run", sinks: [sink, summary] }), operationId: "work:stage-run" });
    await telemetry.startAgent({ backend: "harness" });
    await telemetry.observeEvent({ type: "stage_usage", source: "harness", stage: "executor", requests: 2, reported: 2,
      usage: { inputTokens: 30, outputTokens: 10, totalTokens: 40, coverage: "complete" } });
    await telemetry.observeEvent({ type: "usage", source: "outer", usage: { inputTokens: 60, outputTokens: 20, totalTokens: 80, coverage: "complete" } });
    await telemetry.finishAgent({ status: "ok", outputBytes: 1 });
    expect(sink.observations.find(item => item.kind === "model.stage_usage")).toMatchObject({
      attributes: { "openneko.agent.stage": "executor", "openneko.model.requests": 2 },
      measurements: { totalTokens: 40, coverage: "complete" },
    });
    expect(summary.snapshot().usage.totalTokens).toBe(80);
  });
  it("uses only the Harness remote usage projection for a lookup", async () => {
    const sink = new MemoryObservationSink();
    const telemetry = createAgentEventTelemetry({ observer: createHarnessObserver({ runId: "remote-run", sinks: [sink] }), operationId: "work:remote-run" });
    await telemetry.startAgent({ backend: "harness" });
    await telemetry.observeEvent({ type: "tool_start", id: "harness-operation-1", name: "neko_graphjin_agent" });
    await telemetry.observeEvent({ type: "tool_end", id: "harness-operation-1", result: {
      response: { data: { usage: { total_tokens: 900_000 } } },
    }, remoteUsage: { reported: true, chargedTokens: 6_000, totalTokens: 6_000, promptTokens: 4_000, completionTokens: 2_000, llmCalls: 3 } });
    const inner = sink.observations.find(item => item.kind === "model.response");
    expect(inner?.measurements).toMatchObject({ totalTokens: 6_000, coverage: "complete" });
  });
  it("counts Ax child delegation without double-counting child model usage", async () => {
    const sink = new MemoryObservationSink();
    const telemetry = createAgentEventTelemetry({observer:createHarnessObserver({runId:"child-run",sinks:[sink]}),operationId:"work:child-run"});
    await telemetry.startAgent({backend:"harness"});
    await telemetry.observeEvent({type:"tool_start",id:"harness-child-1",name:"ax_child_agent"});
    await telemetry.observeEvent({type:"tool_end",id:"harness-child-1"});
    await telemetry.observeEvent({type:"usage",source:"outer",usage:{totalTokens:90,coverage:"complete"}});
    await telemetry.finishAgent({status:"ok",outputBytes:4});
    expect(sink.observations.filter(item=>item.kind==="delegation.start")).toHaveLength(1);
    expect(sink.observations.filter(item=>item.kind==="delegation.end")).toHaveLength(1);
    expect(sink.observations.filter(item=>item.kind==="model.request")).toHaveLength(1);
  });
  it("exports metadata and byte counts without prompt, payload, or tool content", async () => {
    const sink = new MemoryObservationSink();
    const observer = createHarnessObserver({ runId: "run-1", sinks: [sink] });
    const telemetry = createAgentEventTelemetry({
      observer,
      operationId: "workflow:run-1",
    });
    const secret = "onk_wf_deadbeefdead_do-not-export-this-token";
    const payload = "customer-private-payload-value";

    await telemetry.startAgent({
      backend: "hermes",
      model: "safe-model-id",
      inputBytes: Buffer.byteLength(payload),
    });
    await telemetry.observeEvent({
      type: "tool_start",
      id: "tool-1",
      name: "neko_graphjin_agent",
      input: { authorization: `Bearer ${secret}`, query: payload },
    });
    await telemetry.observeEvent({
      type: "tool_end",
      id: "tool-1",
      result: { token: secret, rows: [{ private: payload }] },
    });
    await telemetry.observeEvent({
      type: "message",
      role: "assistant",
      content: payload,
    });
    await telemetry.observeEvent({
      type: "status",
      message: "Hermes returned no output; retrying…",
    });
    await telemetry.observeEvent({
      type: "usage",
      source: "outer",
      provider: "provider-id",
      model: "resolved-model-id",
      usage: { totalTokens: 42, coverage: "complete" },
    });
    await telemetry.finishAgent({
      status: "ok",
      outputBytes: Buffer.byteLength(payload),
    });

    const encoded = JSON.stringify(sink.observations);
    expect(encoded).not.toContain(secret);
    expect(encoded).not.toContain(payload);
    expect(encoded).not.toContain("authorization");
    expect(encoded).not.toContain("query");
    expect(sink.observations.map((item) => item.kind)).toEqual(
      expect.arrayContaining([
        "model.request",
        "tool.start",
        "delegation.start",
        "model.response",
        "delegation.end",
        "tool.end",
        "run.first_output",
        "retry",
      ]),
    );
    expect(
      sink.observations.find((item) => item.kind === "tool.start")
        ?.measurements?.inputBytes,
    ).toBeGreaterThan(0);
  });

  it("closes open model, delegation, tool, and stage spans on failure", async () => {
    const sink = new MemoryObservationSink();
    const observer = createHarnessObserver({ runId: "run-2", sinks: [sink] });
    const telemetry = createAgentEventTelemetry({
      observer,
      operationId: "workflow:run-2",
    });

    await telemetry.startAgent({ backend: "hermes" });
    await telemetry.observeEvent({
      type: "tool_start",
      id: "tool-open",
      name: "neko_graphjin_agent",
      input: { value: "private" },
    });
    await telemetry.closeOpen({
      status: "error",
      outcome: "failed",
      errorType: "worker_interrupted",
      usageMissingReason: "model did not complete",
    });

    const endings = sink.observations.filter((item) =>
      ["tool.end", "delegation.end", "model.response", "stage.end"].includes(
        item.kind,
      ),
    );
    expect(endings).toHaveLength(5);
    expect(
      endings.filter((item) => item.kind === "model.response"),
    ).toHaveLength(2);
    expect(endings.every((item) => item.status === "error")).toBe(true);
  });
});
