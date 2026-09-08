import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { readFile } from "node:fs/promises";

const state = vi.hoisted(() => ({ calls: [] as string[][], policy: null as any, fail: "" }));
vi.mock("node:child_process", () => ({
  execFile: (file: string, args: string[], options: unknown, callback: (error: Error | null, value?: { stdout: string }) => void) => {
    state.calls.push(args);
    void (async () => {
      if (args.includes("--policy")) state.policy = JSON.parse(await readFile(args[args.indexOf("--policy") + 1]!, "utf8"));
      if (state.fail && args[1] === state.fail) return callback(new Error("private provider data"));
      callback(null, { stdout: args[1] === "exec" ? '{"ok":true}' : "" });
    })();
  },
}));
import { runPackConnector } from "../src/packs/connector-runner";
const connector = { id: "notes", image: `example.test/notes@sha256:${"a".repeat(64)}`, entrypoint: "/app/connector", operations: [{ id: "read", description: "Read", effect: "read" as const }], network: [] };
beforeEach(() => { state.calls = []; state.fail = ""; vi.stubEnv("OPENSHELL_GATEWAY", ""); vi.stubEnv("OPENSHELL_GATEWAY_ENDPOINT", ""); });
afterEach(() => vi.unstubAllEnvs());
it("uses an explicit policy, private request file and remote timeout, then deletes the sandbox", async () => {
  await expect(runPackConnector(connector, { operation: "read", input: { token: "fixture-private" } })).resolves.toEqual({ ok: true });
  expect(JSON.stringify(state.calls)).not.toContain("fixture-private");
  expect(state.calls.find(args => args[1] === "exec")).toContain("--timeout");
  expect(state.policy.network_policies).toEqual({});
  expect(state.policy.process.run_as_user).toBe("sandbox");
  expect(state.calls.at(-1)?.slice(0, 2)).toEqual(["sandbox", "delete"]);
});
it("rejects writes and unknown operations before starting a process", async () => {
  await expect(runPackConnector(connector, { operation: "missing", input: {} })).rejects.toThrow("not declared");
  await expect(runPackConnector({ ...connector, operations: [{ id: "write", description: "Write", effect: "write" }] }, { operation: "write", input: {} })).rejects.toThrow("action dispatch");
  expect(state.calls).toEqual([]);
});
it("cleans up a failed creation without exposing CLI diagnostics", async () => {
  state.fail = "create";
  await expect(runPackConnector(connector)).rejects.toThrow("create failed");
  expect(state.calls.at(-1)?.slice(0, 2)).toEqual(["sandbox", "delete"]);
});
