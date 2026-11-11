import { trace } from "@opentelemetry/api";
import { InMemorySpanExporter, SimpleSpanProcessor } from "@opentelemetry/sdk-trace-base";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { afterAll, describe, expect, it } from "vitest";
import {
  OpenTelemetryObservationSink,
  createHarnessObserver,
  traceContextHeaders,
  withRunTraceContext,
} from "../src";

const provider = new NodeTracerProvider({
  spanProcessors: [new SimpleSpanProcessor(new InMemorySpanExporter())],
});
provider.register();
afterAll(() => provider.shutdown());

describe("withRunTraceContext", () => {
  it("makes the run's root span the parent of outgoing requests until the run ends", async () => {
    const sink = new OpenTelemetryObservationSink({ tracer: provider.getTracer("test") });
    const observer = createHarnessObserver({ runId: "run-a", sinks: [sink] });
    await observer.observe({ kind: "run.start", operationId: "op-a", timestamp: new Date().toISOString() });

    const headers = await withRunTraceContext("run-a", async () => {
      await Promise.resolve();
      return traceContextHeaders();
    });
    expect(headers.traceparent?.split("-")[1]).toMatch(/^[0-9a-f]{32}$/);
    expect(trace.getActiveSpan()).toBeUndefined();

    await observer.observe({ kind: "run.end", operationId: "op-a", timestamp: new Date().toISOString() });
    expect(withRunTraceContext("run-a", traceContextHeaders)).toEqual({});
  });

  it("forgets a run's span when its sink shuts down", async () => {
    const sink = new OpenTelemetryObservationSink({ tracer: provider.getTracer("test") });
    const observer = createHarnessObserver({ runId: "run-b", sinks: [sink] });
    await observer.observe({ kind: "run.start", operationId: "op-b", timestamp: new Date().toISOString() });
    await sink.shutdown();
    expect(withRunTraceContext("run-b", traceContextHeaders)).toEqual({});
  });

  it("runs unchanged for an unknown run", () => {
    expect(withRunTraceContext("missing", () => 42)).toBe(42);
    expect(withRunTraceContext("missing", traceContextHeaders)).toEqual({});
  });
});
