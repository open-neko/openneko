import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentControlPlane } from "../src/work/control-plane";

const sdk = vi.hoisted(() => ({
  tools: new Map<
    string,
    {
      description: string;
      handler: (args: Record<string, unknown>) => Promise<{
        content: Array<{ type: string; text: string }>;
      }>;
    }
  >(),
}));

vi.mock("../src/mcp-server", () => ({
  createMcpServer: vi.fn((input: unknown) => input),
  defineMcpTool: vi.fn(
    (
      name: string,
      description: string,
      _schema: unknown,
      handler: (
        args: Record<string, unknown>,
      ) => Promise<{ content: Array<{ type: string; text: string }> }>,
    ) => {
      sdk.tools.set(name, { description, handler });
      return { name };
    },
  ),
}));

import { buildPluginActionServer } from "../src/work/tools";

function controlPlane(
  result: Awaited<
    ReturnType<AgentControlPlane["waitForActionExecution"]>
  >,
): Pick<
  AgentControlPlane,
  | "evaluateActionPolicy"
  | "createActionRequest"
  | "enqueueActionExecute"
  | "waitForActionExecution"
> {
  return {
    async evaluateActionPolicy() {
      return {
        decision: "allow",
        mode: "auto",
        reason: "test",
        policy: { id: "policy-1", name: "Read-only web" },
      } as Awaited<
        ReturnType<AgentControlPlane["evaluateActionPolicy"]>
      >;
    },
    async createActionRequest() {
      return { id: "action-1", status: "approved" };
    },
    async enqueueActionExecute() {},
    async waitForActionExecution() {
      return result;
    },
  };
}

async function invoke(
  result: Awaited<
    ReturnType<AgentControlPlane["waitForActionExecution"]>
  >,
) {
  const emit = vi.fn();
  buildPluginActionServer({
    orgId: "org-1",
    threadId: "thread-1",
    runId: "run-1",
    descriptors: [
      {
        kind: "web_search",
        description: "Search the live web.",
        default_mode: "auto",
      },
    ],
    emit,
    controlPlane: controlPlane(result) as AgentControlPlane,
  });
  const registered = sdk.tools.get("web_search");
  if (!registered) throw new Error("web_search tool was not registered");
  const response = await registered.handler({
    payload: { query: "Igatpuri weather" },
  });
  return {
    description: registered.description,
    emit,
    body: JSON.parse(response.content[0]?.text ?? "{}") as Record<
      string,
      unknown
    >,
  };
}

describe("auto-mode plugin action tools", () => {
  beforeEach(() => sdk.tools.clear());

  it("waits for execution and returns the real outcome to the agent", async () => {
    const result = await invoke({
      status: "succeeded",
      outcome: { result: { text: "Rain through Tuesday." } },
    });

    expect(result.body).toMatchObject({
      ok: true,
      status: "executed",
      outcome: { result: { text: "Rain through Tuesday." } },
    });
    expect(result.description).toContain("use its returned outcome");
    expect(result.emit).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "action_request_emit",
        decision: "auto_approved",
      }),
    );
  });

  it("returns execution failures instead of claiming the action is queued", async () => {
    const result = await invoke({
      status: "failed",
      error: "upstream schema rejected the request",
    });

    expect(result.body).toMatchObject({
      ok: false,
      status: "failed",
      error: "upstream schema rejected the request",
    });
    expect(String(result.body.note)).toContain("Do not claim");
  });

  it("labels only genuine timeouts as still running", async () => {
    const result = await invoke({ status: "timeout" });

    expect(result.body).toMatchObject({
      ok: true,
      status: "running",
    });
    expect(String(result.body.note)).toContain("taking longer");
  });

  it("uses a descriptor's fixed internal scope through policy, persistence, and events", async () => {
    const evaluateActionPolicy = vi.fn(async () => ({
      decision: "allow" as const,
      mode: "auto" as const,
      reason: "test",
      policy: { id: "policy-records", name: "Records status" },
    }));
    const createActionRequest = vi.fn(async () => ({ id: "action-records" }));
    const enqueueActionExecute = vi.fn(async () => {});
    const waitForActionExecution = vi.fn(async () => ({
      status: "succeeded" as const,
      outcome: { result: { status: "running" } },
    }));
    const emit = vi.fn();

    buildPluginActionServer({
      orgId: "org-1",
      threadId: "thread-1",
      runId: "run-1",
      descriptors: [
        {
          kind: "records_import_status",
          scope: "internal",
          description: "Read import status.",
          default_mode: { internal: "auto" },
        },
      ],
      emit,
      controlPlane: {
        evaluateActionPolicy,
        createActionRequest,
        enqueueActionExecute,
        waitForActionExecution,
      } as unknown as AgentControlPlane,
    });

    const registered = sdk.tools.get("records_import_status");
    if (!registered) throw new Error("records import tool was not registered");
    await registered.handler({ payload: { import_run_id: "run-1" } });

    const expectedScope = {
      scope: "internal",
      kind: "records_import_status",
    };
    expect(evaluateActionPolicy).toHaveBeenCalledWith(
      expect.objectContaining(expectedScope),
    );
    expect(createActionRequest).toHaveBeenCalledWith(
      expect.objectContaining(expectedScope),
    );
    expect(emit).toHaveBeenCalledWith(expect.objectContaining(expectedScope));
  });
});
