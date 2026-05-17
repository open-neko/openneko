import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  RPC_PROTOCOL_VERSION,
  rpcErr,
  rpcOk,
  type RpcResponse,
} from "@open-neko/plugin-types";
import {
  mockActionAdapter,
  setDefaultActionAdapter,
  type ActionAdapter,
  type ActionRequestRecord,
} from "@neko/llm/workflows";
import { PluginRegistry } from "../../src/plugins/plugin-registry";
import type {
  PluginRuntime,
  PluginVmSpec,
} from "../../src/plugins/microsandbox-runtime";

const FAKE_INTEGRITY = "sha512-" + "a".repeat(86) + "==";

interface RecordedRpc {
  pluginId: string;
  method: string;
  paramsJson: string;
  env?: Record<string, string>;
}

interface FakeRuntimeOptions {
  responses: Partial<Record<string, RpcResponse | ((p: RecordedRpc) => RpcResponse)>>;
}

class FakeRuntime implements PluginRuntime {
  readonly starts: PluginVmSpec[] = [];
  readonly rpcs: RecordedRpc[] = [];
  readonly stopped: string[] = [];
  private readonly running = new Set<string>();
  destroyed = false;

  constructor(private readonly options: FakeRuntimeOptions = { responses: {} }) {}

  hasPlugin(pluginId: string): boolean {
    return this.running.has(pluginId);
  }

  async start(spec: PluginVmSpec): Promise<void> {
    this.starts.push(spec);
    this.running.add(spec.id);
  }

  async callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options?: { env?: Record<string, string> },
  ): Promise<RpcResponse> {
    const rec: RecordedRpc = {
      pluginId,
      method,
      paramsJson,
      ...(options?.env ? { env: options.env } : {}),
    };
    this.rpcs.push(rec);
    const r = this.options.responses[method];
    if (typeof r === "function") return r(rec);
    if (r) return r;
    throw new Error(`FakeRuntime: no response configured for ${method}`);
  }

  async stop(pluginId: string): Promise<void> {
    this.stopped.push(pluginId);
    this.running.delete(pluginId);
  }

  async destroyAll(): Promise<void> {
    this.destroyed = true;
    this.running.clear();
  }
}

async function writeFakeRunner(file: string): Promise<void> {
  await writeFile(file, "// runner — never executed in these tests\n", "utf8");
}

function manifestWithSlackEntry(opts: { kinds?: string[] } = {}) {
  return {
    schema: "https://open-neko.github.io/plugins/manifest.schema.json",
    plugins: [
      {
        name: "@open-neko/plugin-slack",
        version: "0.1.0",
        integrity: FAKE_INTEGRITY,
        capabilities: { network: ["slack.com"] },
        kinds: opts.kinds ?? [
          "send_slack_message",
          "send_slack_dm",
          "react_slack_message",
          "lookup_slack_entity",
        ],
        marketplace: "official",
      },
    ],
  };
}

function fullRegisterResponse(): RpcResponse {
  return rpcOk({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: "@open-neko/plugin-slack",
    pluginVersion: "0.1.0",
    actions: [
      { kind: "send_slack_message", description: "post" },
      { kind: "send_slack_dm", description: "dm" },
      { kind: "react_slack_message", description: "react" },
      { kind: "lookup_slack_entity", description: "lookup" },
    ],
  });
}

function makeRequest(kind: string): ActionRequestRecord {
  return {
    id: `req-${kind}`,
    orgId: "org-1",
    workflowRunId: null,
    triggeredByObservationId: null,
    policyId: null,
    scope: "external",
    kind,
    target: null,
    payload: {},
    riskLevel: "low",
    status: "approved",
    summary: kind,
    requestedByRunId: null,
    approvedByUserId: null,
    approvedAt: new Date(),
    rejectionReason: null,
    createdAt: new Date(),
    updatedAt: new Date(),
  };
}

describe("PluginRegistry", () => {
  let repoRoot: string;
  let workRoot: string;
  let secretsConfigDir: string;
  let runnerPath: string;
  let captured: Map<string, ActionAdapter>;

  beforeEach(async () => {
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-repo-"));
    workRoot = await mkdtemp(path.join(tmpdir(), "openneko-vmwork-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-secrets-"));
    runnerPath = path.join(repoRoot, "fake-runner.js");
    await writeFakeRunner(runnerPath);
    captured = new Map();
    setDefaultActionAdapter(mockActionAdapter);
  });

  afterEach(async () => {
    await rm(repoRoot, { recursive: true, force: true });
    await rm(workRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
    setDefaultActionAdapter(mockActionAdapter);
  });

  function newRegistry(runtime: PluginRuntime, opts: { manifestPlugins?: unknown } = {}) {
    void opts;
    return new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
      onAdapter: (kind, adapter) => captured.set(kind, adapter),
    });
  }

  it("start() with no manifest reports zero plugins, zero kinds", async () => {
    const runtime = new FakeRuntime();
    const reg = newRegistry(runtime);
    await reg.start();
    expect(reg.status()).toEqual({
      loaded: [],
      skipped: [],
      kinds: [],
      vmsRunning: 0,
    });
    await reg.stop();
  });

  it("registers adapters for every declared kind LAZILY — no VM until first action", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime();
    const reg = newRegistry(runtime);
    await reg.start();

    expect(reg.status().kinds.sort()).toEqual([
      "lookup_slack_entity",
      "react_slack_message",
      "send_slack_dm",
      "send_slack_message",
    ]);
    expect(reg.status().vmsRunning).toBe(0);
    expect(runtime.starts).toEqual([]);
    expect(captured.size).toBe(4);
    await reg.stop();
  });

  it("refresh after the manifest gains a plugin registers new kinds WITHOUT restart", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [],
      }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.status().kinds).toEqual([]);

    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    await reg.refresh();
    expect(reg.status().kinds).toContain("send_slack_message");
    expect(captured.has("send_slack_message")).toBe(true);
    await reg.stop();
  });

  it("refresh after a manifest entry is removed stops the plugin's VM", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: fullRegisterResponse(),
        execute_action: rpcOk({
          outcome: { result: {}, externalRef: null, commandOrOperation: null },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    const adapter = captured.get("send_slack_message")!;
    await adapter({ request: makeRequest("send_slack_message") });
    expect(runtime.starts).toHaveLength(1);
    expect(reg.status().vmsRunning).toBe(1);

    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [],
      }),
      "utf8",
    );
    await reg.refresh();
    expect(runtime.stopped).toContain("open-neko-plugin-slack");
    expect(reg.status().kinds).toEqual([]);
    await reg.stop();
  });

  it("spawns the VM on first execute_action, reuses it on subsequent calls", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: fullRegisterResponse(),
        execute_action: rpcOk({
          outcome: { result: { ok: true }, externalRef: null, commandOrOperation: null },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    const adapter = captured.get("send_slack_message")!;
    await adapter({ request: makeRequest("send_slack_message") });
    expect(runtime.starts).toHaveLength(1);
    await adapter({ request: makeRequest("send_slack_message") });
    expect(runtime.starts).toHaveLength(1);
    expect(runtime.rpcs.filter((r) => r.method === "execute_action")).toHaveLength(2);
    await reg.stop();
  });

  it("scrubber rebuilds when the secrets file changes (refresh-driven)", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    await writeFile(
      path.join(secretsConfigDir, "secrets.json"),
      JSON.stringify({
        "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "xoxb-original" },
      }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.getScrubber()("token=xoxb-original")).toBe("token=[REDACTED]");

    await writeFile(
      path.join(secretsConfigDir, "secrets.json"),
      JSON.stringify({
        "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "xoxb-rotated" },
      }),
      "utf8",
    );
    await reg.refresh();
    expect(reg.getScrubber()("token=xoxb-rotated")).toBe("token=[REDACTED]");
    // Old value no longer in store → no longer auto-redacted (documented).
    expect(reg.getScrubber()("token=xoxb-original")).toBe("token=xoxb-original");
    await reg.stop();
  });

  it("passes per-plugin env from the secrets store on execute_action", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    await writeFile(
      path.join(secretsConfigDir, "secrets.json"),
      JSON.stringify({
        "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "xoxb-secret" },
      }),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: fullRegisterResponse(),
        execute_action: rpcOk({
          outcome: { result: {}, externalRef: null, commandOrOperation: null },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    await captured.get("send_slack_message")!({
      request: makeRequest("send_slack_message"),
    });
    const exec = runtime.rpcs.find((r) => r.method === "execute_action");
    expect(exec?.env).toEqual({ SLACK_BOT_TOKEN: "xoxb-secret" });
    await reg.stop();
  });

  it("rejects a VM whose register() declares fewer kinds than the manifest", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(
        manifestWithSlackEntry({
          kinds: ["send_slack_message", "lookup_slack_entity"],
        }),
      ),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: rpcOk({
          protocol: RPC_PROTOCOL_VERSION,
          pluginName: "@open-neko/plugin-slack",
          pluginVersion: "0.1.0",
          actions: [{ kind: "send_slack_message", description: "post" }],
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    await expect(
      captured.get("send_slack_message")!({
        request: makeRequest("send_slack_message"),
      }),
    ).rejects.toThrow(/manifest declares kinds/);
    await reg.stop();
  });

  it("warns + skips when two plugins claim the same kind", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [
          {
            name: "@open-neko/plugin-slack",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            capabilities: { network: ["slack.com"] },
            kinds: ["send_slack_message"],
          },
          {
            name: "@acme/plugin-slack-alt",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            capabilities: { network: ["slack.com"] },
            kinds: ["send_slack_message"],
          },
        ],
      }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.status().skipped.length).toBe(1);
    expect(reg.status().skipped[0]?.reason).toMatch(/already claimed/);
    await reg.stop();
  });

  it("malformed manifest is treated as empty (worker keeps running)", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      "not json",
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.status().loaded).toEqual([]);
    await reg.stop();
  });

  it("surfaces register() failures with a clear plugin-specific message", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: { register: rpcErr("BAD", "register failed") },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    await expect(
      captured.get("send_slack_message")!({
        request: makeRequest("send_slack_message"),
      }),
    ).rejects.toThrow(/register\(\) failed/);
    await reg.stop();
  });
});
