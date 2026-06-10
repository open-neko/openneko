import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { AgentEvent } from "@neko/llm";
import type { AgentControlPlane } from "@neko/llm/work";
import { createAgentBroker, type RunBinding } from "../../src/agent-sandbox/broker";
import {
  BrokerControlPlane,
  postAgentEvents,
} from "../../src/agent-sandbox/broker-client";

interface Call {
  method: string;
  input: Record<string, unknown>;
}

function makeFakeControlPlane() {
  const calls: Call[] = [];
  const cp: AgentControlPlane = {
    async evaluateActionPolicy(input) {
      calls.push({ method: "evaluate", input: input as Record<string, unknown> });
      return { decision: "allow", policy: { id: "p1", name: "P" }, mode: "auto" } as Awaited<
        ReturnType<AgentControlPlane["evaluateActionPolicy"]>
      >;
    },
    async createActionRequest(input) {
      calls.push({ method: "create", input: input as Record<string, unknown> });
      return { id: "ar1" };
    },
    async enqueueActionExecute(input) {
      calls.push({ method: "enqueue", input });
    },
    async rememberWorkMemory(input) {
      calls.push({ method: "remember", input: input as Record<string, unknown> });
      return { id: "m1" };
    },
    async searchWorkMemoryByContext(args) {
      calls.push({ method: "search", input: args as Record<string, unknown> });
      return [] as Awaited<
        ReturnType<AgentControlPlane["searchWorkMemoryByContext"]>
      >;
    },
    async saveWorkflowWithTrigger(input) {
      calls.push({ method: "wf-save", input: input as Record<string, unknown> });
      return { action: "created", workflow: { id: "w1", name: "W" } } as Awaited<
        ReturnType<AgentControlPlane["saveWorkflowWithTrigger"]>
      >;
    },
    async listWorkflowsWithTriggers(input) {
      calls.push({ method: "wf-list", input: input as Record<string, unknown> });
      return { total: 0, workflows: [] };
    },
    async upsertActionPolicyByName(input) {
      calls.push({ method: "rule-save", input: input as Record<string, unknown> });
      return { action: "created", policy: { id: "p1", name: "R" } } as Awaited<
        ReturnType<AgentControlPlane["upsertActionPolicyByName"]>
      >;
    },
    async listActionPolicies(input) {
      calls.push({ method: "rule-list", input: input as Record<string, unknown> });
      return { total: 0, policies: [] };
    },
  };
  return { cp, calls };
}

describe("agent broker", () => {
  let server: Server;
  let baseUrl: string;
  let fake: ReturnType<typeof makeFakeControlPlane>;
  let events: Array<{ binding: RunBinding; evs: AgentEvent[] }>;

  beforeEach(async () => {
    fake = makeFakeControlPlane();
    events = [];
    server = createAgentBroker({
      controlPlane: fake.cp,
      resolveRun: (t) => (t === "good" ? { runId: "r1", orgId: "o1" } : undefined),
      onEvents: async (binding, evs) => {
        events.push({ binding, evs });
      },
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });

  afterEach(() => new Promise<void>((r) => server.close(() => r())));

  it("forces orgId/workRunId from the token binding, not the request body", async () => {
    const cp = new BrokerControlPlane(baseUrl, "good");
    await cp.evaluateActionPolicy({
      orgId: "SPOOF",
      scope: "external",
      kind: "k",
      target: null,
      riskLevel: null,
    } as Parameters<AgentControlPlane["evaluateActionPolicy"]>[0]);
    await cp.createActionRequest({
      orgId: "SPOOF",
      workRunId: "SPOOF",
      scope: "external",
      kind: "k",
      payload: {},
      status: "approved",
    } as Parameters<AgentControlPlane["createActionRequest"]>[0]);
    await cp.searchWorkMemoryByContext({ orgId: "SPOOF", query: "q" } as Parameters<
      AgentControlPlane["searchWorkMemoryByContext"]
    >[0]);

    expect(fake.calls.find((c) => c.method === "evaluate")?.input.orgId).toBe("o1");
    const create = fake.calls.find((c) => c.method === "create")?.input;
    expect(create?.orgId).toBe("o1");
    expect(create?.workRunId).toBe("r1");
    expect(fake.calls.find((c) => c.method === "search")?.input.orgId).toBe("o1");
  });

  it("round-trips return values and forces enqueue orgId", async () => {
    const cp = new BrokerControlPlane(baseUrl, "good");
    expect(
      await cp.createActionRequest({
        scope: "external",
        kind: "k",
        payload: {},
        status: "approved",
      } as Parameters<AgentControlPlane["createActionRequest"]>[0]),
    ).toEqual({ id: "ar1" });
    await cp.enqueueActionExecute({ orgId: "ignored", actionRequestId: "ar1" });
    expect(fake.calls.find((c) => c.method === "enqueue")?.input).toEqual({
      orgId: "o1",
      actionRequestId: "ar1",
    });
  });

  it("forces builder save/list org + run provenance from the binding", async () => {
    const cp = new BrokerControlPlane(baseUrl, "good");
    await cp.saveWorkflowWithTrigger({
      orgId: "SPOOF",
      createdByRunId: "SPOOF",
      name: "n",
      steps: [],
    } as unknown as Parameters<AgentControlPlane["saveWorkflowWithTrigger"]>[0]);
    await cp.upsertActionPolicyByName({
      orgId: "SPOOF",
      createdByRunId: "SPOOF",
      name: "r",
      description: "",
      appliesToKinds: [],
      appliesToScopes: [],
      mode: "approval_required",
      priority: 0,
      enabled: true,
    } as unknown as Parameters<AgentControlPlane["upsertActionPolicyByName"]>[0]);
    await cp.listWorkflowsWithTriggers({ orgId: "SPOOF" });
    await cp.listActionPolicies({ orgId: "SPOOF" });

    const wfSave = fake.calls.find((c) => c.method === "wf-save")?.input;
    expect(wfSave?.orgId).toBe("o1");
    expect(wfSave?.createdByRunId).toBe("r1");
    const ruleSave = fake.calls.find((c) => c.method === "rule-save")?.input;
    expect(ruleSave?.orgId).toBe("o1");
    expect(ruleSave?.createdByRunId).toBe("r1");
    expect(fake.calls.find((c) => c.method === "wf-list")?.input.orgId).toBe("o1");
    expect(fake.calls.find((c) => c.method === "rule-list")?.input.orgId).toBe("o1");
  });

  it("rejects an invalid token (401)", async () => {
    const cp = new BrokerControlPlane(baseUrl, "bad");
    await expect(
      cp.createActionRequest({
        scope: "external",
        kind: "k",
        payload: {},
        status: "approved",
      } as Parameters<AgentControlPlane["createActionRequest"]>[0]),
    ).rejects.toThrow(/401/);
  });

  it("delivers events to the host sink with the run binding", async () => {
    await postAgentEvents(baseUrl, "good", [
      { type: "message", text: "hi" } as unknown as AgentEvent,
    ]);
    expect(events).toHaveLength(1);
    expect(events[0]?.binding).toEqual({ runId: "r1", orgId: "o1" });
    expect(events[0]?.evs[0]).toMatchObject({ type: "message", text: "hi" });
  });

  it("rejects events from an invalid token", async () => {
    await expect(
      postAgentEvents(baseUrl, "bad", [{ type: "message", text: "x" } as unknown as AgentEvent]),
    ).rejects.toThrow(/401/);
    expect(events).toHaveLength(0);
  });
});
