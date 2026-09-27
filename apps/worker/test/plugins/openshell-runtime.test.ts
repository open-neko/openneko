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
      "/usr/local/bin/node",
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
      image: "ghcr.io/open-neko/plugin-base:node24",
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
      "ghcr.io/open-neko/plugin-base:node24",
      "--no-tty",
      "--no-auto-providers",
      "--",
      "sleep",
      "infinity",
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

  it("replaces a stale gateway sandbox when create collides on the name", async () => {
    const rt = make();
    let creates = 0;
    h.state.respond = (args) => {
      if (args[1] === "create") {
        creates += 1;
        return creates === 1
          ? { stderr: "Error: × sandbox 'p1' already exists", code: 1 }
          : { stdout: "", code: 0 };
      }
      return { stdout: "", code: 0 };
    };

    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });

    // create (collides) → delete the corpse → create again → upload.
    expect(h.calls.map((c) => c.args[1])).toEqual([
      "create",
      "delete",
      "create",
      "upload",
    ]);
    expect(rt.hasPlugin("p1")).toBe(true);
  });

  it("surfaces non-collision create failures unchanged", async () => {
    const rt = make();
    h.state.respond = (args) =>
      args[1] === "create"
        ? { stderr: "Error: manifest unknown", code: 1 }
        : { stdout: "", code: 0 };

    await expect(
      rt.start({
        id: "p1",
        hostWorkspacePath: "/tmp/bundles/p1",
        network: "none",
        hosts: [],
      }),
    ).rejects.toThrow(/manifest unknown/);
    expect(h.calls.map((c) => c.args[1])).toEqual(["create"]);
    expect(rt.hasPlugin("p1")).toBe(false);
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

  it("callRpc with env uploads a sourced env file — the secret never enters the exec command", async () => {
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

    // The secret rode an out-of-band upload to the env file (the upload argv is
    // the host temp PATH + dest path, never the value) — not the exec command.
    const upload = h.calls.find(
      (c) =>
        c.args[0] === "sandbox" &&
        c.args[1] === "upload" &&
        c.args.includes("/sandbox/.plugin-env"),
    );
    expect(upload).toBeDefined();

    const args = execCall()?.args ?? [];
    expect(args[8]).toBe("sh");
    expect(args[9]).toBe("-c");
    expect(args[10]).toContain(". '/sandbox/.plugin-env';");
    expect(args[10]).toContain("exec node /sandbox/run.js");
    // The token value appears in NO spawned command's argv (gateway-log safe).
    expect(h.calls.some((c) => c.args.some((a) => a.includes("xoxb-1")))).toBe(false);
  });

  it("re-uses the uploaded env file across calls with unchanged env (no re-upload)", async () => {
    const rt = make();
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "none",
      hosts: [],
    });
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };
    const env = { SLACK_BOT_TOKEN: "xoxb-1" };
    await rt.callRpc("p1", "m1", "{}", { env });
    h.calls.length = 0;
    await rt.callRpc("p1", "m2", "{}", { env });
    expect(h.calls.some((c) => c.args[1] === "upload")).toBe(false);
  });

  it("egress secret: registers a provider, creates with --provider, and keeps the value out of the box", async () => {
    const rt = make({ gatewayName: "openneko" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "public",
      hosts: ["api.telegram.org"],
      egressSecrets: [{ key: "TELEGRAM_BOT_TOKEN", value: "12345:SEKRIT" }],
    });

    // The real value is registered gateway-side (host → gateway, never a sandbox)…
    expect(
      h.calls.some((c) => c.args.includes("provider") && c.args.includes("create")),
    ).toBe(true);
    // …and the sandbox is created attached to that provider.
    const create = h.calls.find(
      (c) => c.args.includes("sandbox") && c.args.includes("create"),
    );
    expect(create?.args).toContain("--provider");
    expect(create?.args).toContain("plugin-p1");

    // A call aliases the placeholder to the key, uploads NO env file for it, and
    // the value never appears in any box-bound command (exec / upload).
    h.calls.length = 0;
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };
    await rt.callRpc("p1", "deliver", "{}", {
      env: { TELEGRAM_BOT_TOKEN: "12345:SEKRIT" },
    });

    expect(h.calls.some((c) => c.args[1] === "upload")).toBe(false);
    const inner = (execCall()?.args ?? []).at(-1) ?? "";
    expect(inner).toContain('export TELEGRAM_BOT_TOKEN="$c0"');
    expect(inner).toContain("exec node /sandbox/run.js");
    expect(inner).not.toContain("SEKRIT");
    const boxCalls = h.calls.filter(
      (c) => c.args.includes("exec") || c.args[1] === "upload",
    );
    expect(boxCalls.some((c) => c.args.some((a) => a.includes("SEKRIT")))).toBe(false);
  });

  it("rotates the gateway-side provider when the egress value changes — no VM restart", async () => {
    const rt = make({ gatewayName: "openneko" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "public",
      hosts: ["api.telegram.org"],
      egressSecrets: [{ key: "TELEGRAM_BOT_TOKEN", value: "tok-v1" }],
    });
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };

    // Unchanged value → no provider round-trip.
    h.calls.length = 0;
    await rt.callRpc("p1", "deliver", "{}", { env: { TELEGRAM_BOT_TOKEN: "tok-v1" } });
    expect(h.calls.some((c) => c.args.includes("provider"))).toBe(false);

    // Rotated value → provider refreshed, sandbox NOT re-created, value not in the box.
    h.calls.length = 0;
    await rt.callRpc("p1", "deliver", "{}", { env: { TELEGRAM_BOT_TOKEN: "tok-v2" } });
    expect(h.calls.some((c) => c.args.includes("provider"))).toBe(true);
    expect(
      h.calls.some((c) => c.args.includes("sandbox") && c.args.includes("create")),
    ).toBe(false);
    const boxCalls = h.calls.filter(
      (c) => c.args.includes("exec") || c.args[1] === "upload",
    );
    expect(boxCalls.some((c) => c.args.some((a) => a.includes("tok-v2")))).toBe(false);
  });

  it("supports multiple egress secrets — one credential slot each, distinct aliases, no value in the box", async () => {
    const rt = make({ gatewayName: "openneko" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "public",
      hosts: ["slack.com"],
      egressSecrets: [
        { key: "SLACK_BOT_TOKEN", value: "xoxb-SEKRIT0" },
        { key: "SLACK_APP_TOKEN", value: "xapp-SEKRIT1" },
      ],
    });

    // The provider was created with both credential slots (host → gateway).
    const create = h.calls.find((c) => c.args.includes("provider") && c.args.includes("create"));
    expect(create?.args).toContain("c0=xoxb-SEKRIT0");
    expect(create?.args).toContain("c1=xapp-SEKRIT1");

    h.calls.length = 0;
    h.state.respond = (args) =>
      args.includes("exec") ? { stdout: OK_JSON } : { stdout: "", code: 0 };
    await rt.callRpc("p1", "deliver", "{}", {
      env: { SLACK_BOT_TOKEN: "xoxb-SEKRIT0", SLACK_APP_TOKEN: "xapp-SEKRIT1" },
    });

    const inner = (execCall()?.args ?? []).at(-1) ?? "";
    expect(inner).toContain('export SLACK_BOT_TOKEN="$c0"');
    expect(inner).toContain('export SLACK_APP_TOKEN="$c1"');
    expect(h.calls.some((c) => c.args[1] === "upload")).toBe(false);
    const boxCalls = h.calls.filter((c) => c.args.includes("exec") || c.args[1] === "upload");
    expect(boxCalls.some((c) => c.args.some((a) => a.includes("SEKRIT")))).toBe(false);
  });

  it("stop deletes the egress-secret provider too", async () => {
    const rt = make({ gatewayName: "openneko" });
    await rt.start({
      id: "p1",
      hostWorkspacePath: "/tmp/bundles/p1",
      network: "public",
      hosts: ["api.telegram.org"],
      egressSecrets: [{ key: "TELEGRAM_BOT_TOKEN", value: "v" }],
    });
    h.calls.length = 0;
    await rt.stop("p1");
    expect(
      h.calls.some(
        (c) =>
          c.args.includes("provider") &&
          c.args.includes("delete") &&
          c.args.includes("plugin-p1"),
      ),
    ).toBe(true);
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
