import { afterEach, describe, expect, it } from "vitest";
import type { AgentControlPlane } from "../src/work/control-plane";
import { ensureAgentBroker, startAgentBroker } from "../src/work/broker";

// /v1/events is the only path exercised here and it never touches the control
// plane (it routes to deps.onEvents), so a stub that satisfies the interface
// is enough — the registry/auth behaviour is what's under test.
function stubControlPlane(): AgentControlPlane {
  const unused = () => {
    throw new Error("not exercised by these tests");
  };
  return {
    evaluateActionPolicy: unused as AgentControlPlane["evaluateActionPolicy"],
    createActionRequest: unused as AgentControlPlane["createActionRequest"],
    enqueueActionExecute: unused as AgentControlPlane["enqueueActionExecute"],
    rememberWorkMemory: unused as AgentControlPlane["rememberWorkMemory"],
    searchWorkMemoryByContext:
      unused as AgentControlPlane["searchWorkMemoryByContext"],
  };
}

function postEvents(port: number, token: string): Promise<Response> {
  return fetch(new URL("/v1/events", `http://127.0.0.1:${port}`), {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify({ events: [] }),
  });
}

describe("startAgentBroker token registry", () => {
  it("mints one stable token per run and resolves it over HTTP", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const a = handle.tokenFor({ runId: "r1", orgId: "o1" });
      expect(handle.tokenFor({ runId: "r1", orgId: "o1" })).toBe(a); // reused
      expect(handle.tokenFor({ runId: "r2", orgId: "o1" })).not.toBe(a); // per-run

      expect(handle.url).toBe(`http://host.openshell.internal:${handle.port}`);

      expect((await postEvents(handle.port, a)).status).toBe(200);
      expect((await postEvents(handle.port, "unknown")).status).toBe(401);
    } finally {
      await handle.close();
    }
  });

  it("releases a run's token so it stops resolving", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const tok = handle.tokenFor({ runId: "r1", orgId: "o1" });
      handle.release("r1");
      expect((await postEvents(handle.port, tok)).status).toBe(401);
      // a token minted after release is a fresh one, not the revoked value:
      expect(handle.tokenFor({ runId: "r1", orgId: "o1" })).not.toBe(tok);
    } finally {
      await handle.close();
    }
  });

  it("advertises a custom host alias in the url", async () => {
    const handle = await startAgentBroker({
      controlPlane: stubControlPlane(),
      port: 0,
      hostAlias: "10.200.0.1",
    });
    try {
      expect(handle.url).toBe(`http://10.200.0.1:${handle.port}`);
    } finally {
      await handle.close();
    }
  });
});

describe("ensureAgentBroker gating", () => {
  const prev = process.env.OPENNEKO_AGENT_RUNTIME;
  afterEach(() => {
    if (prev === undefined) delete process.env.OPENNEKO_AGENT_RUNTIME;
    else process.env.OPENNEKO_AGENT_RUNTIME = prev;
  });

  it("returns undefined unless OPENNEKO_AGENT_RUNTIME=openshell", async () => {
    delete process.env.OPENNEKO_AGENT_RUNTIME;
    await expect(ensureAgentBroker()).resolves.toBeUndefined();
    process.env.OPENNEKO_AGENT_RUNTIME = "inprocess";
    await expect(ensureAgentBroker()).resolves.toBeUndefined();
  });
});
