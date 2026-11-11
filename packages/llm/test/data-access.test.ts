import { afterEach, describe, expect, it } from "vitest";
import { dataBackend, setDataBackend, type DataBackend } from "../src/data-access";
import { inProcessControlPlane } from "../src/work/control-plane";

function fakeBackend() {
  const calls: Array<{ method: string; input: unknown }> = [];
  const backend: DataBackend = {
    name: "fake",
    async query(input) {
      calls.push({ method: "query", input });
      return { data: { rows: [1] } };
    },
    async listTools(caller) {
      calls.push({ method: "listTools", input: caller });
      return [{ name: "list_tables", inputSchema: { type: "object" } }];
    },
    async callTool(input) {
      calls.push({ method: "callTool", input });
      return { content: [{ type: "text", text: "ok" }] };
    },
  };
  return { backend, calls };
}

afterEach(() => setDataBackend(undefined));

describe("data access interface", () => {
  it("uses GraphJin when no other backend is set", () => {
    expect(dataBackend().name).toBe("graphjin");
    setDataBackend(fakeBackend().backend);
    expect(dataBackend().name).toBe("fake");
    setDataBackend(undefined);
    expect(dataBackend().name).toBe("graphjin");
  });

  it("sends the agent's data calls from the control plane to the backend", async () => {
    const { backend, calls } = fakeBackend();
    setDataBackend(backend);
    await expect(
      inProcessControlPlane.queryGraphjinRead({ orgId: "org-a", query: "{ orders { id } }", variables: { n: 1 } }),
    ).resolves.toEqual({ data: { rows: [1] } });
    await expect(inProcessControlPlane.listGraphjinTools({ orgId: "org-a" })).resolves.toHaveLength(1);
    await expect(
      inProcessControlPlane.callGraphjinTool({ orgId: "org-a", name: "list_tables", arguments: { limit: 5 } }),
    ).resolves.toEqual({ content: [{ type: "text", text: "ok" }] });
    expect(calls).toEqual([
      { method: "query", input: { orgId: "org-a", query: "{ orders { id } }", variables: { n: 1 } } },
      { method: "listTools", input: { orgId: "org-a" } },
      { method: "callTool", input: { orgId: "org-a", name: "list_tables", arguments: { limit: 5 } } },
    ]);
  });

  it("refuses a write in the read path before the backend sees it", async () => {
    const { backend, calls } = fakeBackend();
    setDataBackend(backend);
    for (const query of ["mutation { delete_orders { id } }", "subscription { orders { id } }", "fragment F on O { id }", " "]) {
      await expect(inProcessControlPlane.queryGraphjinRead({ orgId: "org-a", query })).rejects.toThrow();
    }
    expect(calls).toEqual([]);
  });
});
