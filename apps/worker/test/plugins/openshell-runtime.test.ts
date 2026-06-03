import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The runtime shells out to the `openshell` CLI via node:child_process
 * spawn. We mock spawn to record argv and return scripted stdout/exit,
 * asserting the exact sandbox create/upload/exec/delete + policy commands.
 */
const h = vi.hoisted(() => {
  const calls: { args: string[] }[] = [];
  const state = {
    respond: (_args: string[]) =>
      ({ stdout: "", code: 0 }) as {
        stdout?: string;
        stderr?: string;
        code?: number;
        delayMs?: number;
      },
  };
  function spawn(_cmd: string, args: string[]) {
    calls.push({ args });
    const res = state.respond(args);
    const reg = (store: Record<string, Array<(...a: unknown[]) => void>>) =>
      (ev: string, cb: (...a: unknown[]) => void) => {
        (store[ev] ??= []).push(cb);
      };
    const fire = (
      store: Record<string, Array<(...a: unknown[]) => void>>,
      ev: string,
      ...a: unknown[]
    ) => (store[ev] ?? []).forEach((cb) => cb(...a));
    const so = {}, se = {}, ch = {};
    setTimeout(() => {
      if (res.stdout) fire(so, "data", Buffer.from(res.stdout));
      if (res.stderr) fire(se, "data", Buffer.from(res.stderr));
      fire(ch, "close", res.code ?? 0);
    }, res.delayMs ?? 0);
    return { stdout: { on: reg(so) }, stderr: { on: reg(se) }, on: reg(ch), kill() {} };
  }
  return { calls, state, spawn };
});

vi.mock("node:child_process", () => ({ spawn: h.spawn }));

const { OpenShellRuntime, buildPolicyUpdateArgs } = await import(
  "../../src/plugins/openshell-runtime"
);

const OK_JSON = JSON.stringify({ ok: true, result: { hello: "world" } });

function execCall() {
  return h.calls.find((c) => c.args.includes("exec"));
}

describe("buildPolicyUpdateArgs", () => {
  it("returns null when no hosts are declared (inherits default-deny)", () => {
    expect(buildPolicyUpdateArgs("p1", [])).toBeNull();
  });

  it("emits per-host endpoints scoped to node + all-path allows", () => {
    expect(buildPolicyUpdateArgs("p1", ["slack.com", "api.slack.com"])).toEqual([
      "policy",
      "update",
      "p1",
      "--add-endpoint",
      "slack.com:443:read-write:rest:enforce",
      "--add-endpoint",
      "api.slack.com:443:read-write:rest:enforce",
      "--binary",
      "node",
      "--add-allow",
      "slack.com:443:*:/**",
      "--add-allow",
      "api.slack.com:443:*:/**",
      "--wait",
      "--timeout",
      "60",
    ]);
  });
});

describe("OpenShellRuntime", () => {
  beforeEach(() => {
    h.calls.length = 0;
    h.state.respond = () => ({ stdout: "", code: 0 });
  });

  function make(opts?: { gatewayEndpoint?: string; gatewayName?: string }) {
    return new OpenShellRuntime({
      image: "ghcr.io/open-neko/plugin-base:node20",
      bundleDir: "/tmp/bundles",
      ...opts,
    });
  }

  it("create + upload + policy update on start, in order", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "public",
      hosts: ["slack.com"],
    });

    expect(h.calls[0]?.args).toEqual([
      "sandbox",
      "create",
      "--name",
      "p1",
      "--from",
      "ghcr.io/open-neko/plugin-base:node20",
      "--no-tty",
      "--no-auto-providers",
      "--",
      "node",
      "--version",
    ]);
    expect(h.calls[1]?.args).toEqual([
      "sandbox",
      "upload",
      "p1",
      "/tmp/bundles/p1/run.js",
      "/sandbox/run.js",
    ]);
    expect(h.calls[2]?.args).toEqual(buildPolicyUpdateArgs("p1", ["slack.com"]));
    expect(rt.hasPlugin("p1")).toBe(true);
  });

  it("skips the policy update when no hosts are declared", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    expect(h.calls.map((c) => c.args[1])).toEqual(["create", "upload"]);
    expect(h.calls.some((c) => c.args[0] === "policy")).toBe(false);
  });

  it("start is idempotent", async () => {
    const rt = make();
    const spec = {
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none" as const,
      hosts: [],
    };
    await rt.start(spec);
    h.calls.length = 0;
    await rt.start(spec);
    expect(h.calls).toHaveLength(0);
  });

  it("callRpc without env exec's node directly and parses last-line JSON", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    h.calls.length = 0;
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };

    const res = await rt.callRpc("p1", "register", "{}");

    expect(execCall()?.args).toEqual([
      "sandbox",
      "exec",
      "-n",
      "p1",
      "--no-tty",
      "--timeout",
      "30",
      "--",
      "node",
      "/sandbox/run.js",
      "register",
      "{}",
    ]);
    expect(res).toEqual({ ok: true, result: { hello: "world" } });
  });

  it("callRpc with env wraps in sh -c with quoted exports", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    h.calls.length = 0;
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };

    await rt.callRpc("p1", "execute_action", '{"a":1}', {
      env: { SLACK_BOT_TOKEN: "xoxb-1" },
    });

    const args = execCall()?.args ?? [];
    expect(args.slice(0, 8)).toEqual([
      "sandbox",
      "exec",
      "-n",
      "p1",
      "--no-tty",
      "--timeout",
      "30",
      "--",
    ]);
    expect(args[8]).toBe("sh");
    expect(args[9]).toBe("-c");
    expect(args[10]).toContain("export SLACK_BOT_TOKEN='xoxb-1'");
    expect(args[10]).toContain("exec node /sandbox/run.js");
  });

  it("parses the LAST stdout line when logs precede the JSON", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    h.state.respond = (args) =>
      args.includes("exec")
        ? { stdout: `[plugin-log] starting\nmore noise\n${OK_JSON}` }
        : { stdout: "", code: 0 };

    const res = await rt.callRpc("p1", "register", "{}");
    expect(res).toEqual({ ok: true, result: { hello: "world" } });
  });

  it("returns an RpcErr when the plugin exits non-zero but prints JSON", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    const errJson = JSON.stringify({
      ok: false,
      error: { code: "boom", message: "bad" },
    });
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: errJson, code: 1 } : { stdout: "", code: 0 };

    const res = await rt.callRpc("p1", "execute_action", "{}");
    expect(res).toEqual({ ok: false, error: { code: "boom", message: "bad" } });
  });

  it("throws on non-JSON stdout", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: "not json" } : { stdout: "", code: 0 };

    await expect(rt.callRpc("p1", "register", "{}")).rejects.toThrow(/non-JSON/);
  });

  it("prepends --gateway-endpoint when configured", async () => {
    const rt = make({ gatewayEndpoint: "https://gw:17670" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    expect(h.calls[0]?.args.slice(0, 2)).toEqual([
      "--gateway-endpoint",
      "https://gw:17670",
    ]);
    expect(h.calls[0]?.args[2]).toBe("sandbox");
  });

  it("prefers --gateway <name> over --gateway-endpoint (mTLS path)", async () => {
    const rt = make({ gatewayName: "openneko", gatewayEndpoint: "https://gw:18080" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    expect(h.calls[0]?.args.slice(0, 2)).toEqual(["--gateway", "openneko"]);
    expect(h.calls[0]?.args).not.toContain("--gateway-endpoint");
  });

  it("stop deletes the sandbox and clears hasPlugin; destroyAll clears all", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    await rt.start({
      id: "p2",
      hostWorkspacePath: "/tmp/bundles/p2",
      network: "none",
      hosts: [],
    });
    h.calls.length = 0;

    await rt.stop("p1");
    expect(h.calls[0]?.args).toEqual(["sandbox", "delete", "p1"]);
    expect(rt.hasPlugin("p1")).toBe(false);
    expect(rt.hasPlugin("p2")).toBe(true);

    await rt.destroyAll();
    expect(rt.hasPlugin("p2")).toBe(false);
  });
});
