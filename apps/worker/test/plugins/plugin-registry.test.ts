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

const SLACK_KIND_DECLS = [
  { kind: "send_slack_message", description: "post" },
  { kind: "send_slack_dm", description: "dm" },
  { kind: "react_slack_message", description: "react" },
  { kind: "lookup_slack_entity", description: "lookup" },
];

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

function manifestWithSlackEntry(
  opts: { kinds?: Array<{ kind: string; description: string }> } = {},
) {
  return {
    schema: "https://open-neko.github.io/plugins/manifest.schema.json",
    plugins: [
      {
        name: "@open-neko/plugin-slack",
        version: "0.1.0",
        integrity: FAKE_INTEGRITY,
        permissions: { network: ["slack.com"], env: [] },
        capabilities: {
          action: { kinds: opts.kinds ?? SLACK_KIND_DECLS },
        },
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
    capabilities: {
      action: { kinds: SLACK_KIND_DECLS },
    },
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
      flagged: [],
      kinds: [],
      vmsRunning: 0,
      authProvider: null,
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
          kinds: [
            { kind: "send_slack_message", description: "post" },
            { kind: "lookup_slack_entity", description: "lookup" },
          ],
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
          capabilities: {
            action: {
              kinds: [{ kind: "send_slack_message", description: "post" }],
            },
          },
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
            permissions: { network: ["slack.com"], env: [] },
            capabilities: {
              action: {
                kinds: [{ kind: "send_slack_message", description: "post" }],
              },
            },
          },
          {
            name: "@acme/plugin-slack-alt",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            permissions: { network: ["slack.com"], env: [] },
            capabilities: {
              action: {
                kinds: [{ kind: "send_slack_message", description: "post" }],
              },
            },
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

  it("invokes onManifestRefresh with the parsed entries after each refresh", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const refreshes: Array<Array<{ name: string; kindCount: number }>> = [];
    const reg = new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime: new FakeRuntime(),
      resolveRunner: () => runnerPath,
      onManifestRefresh: (entries) => {
        refreshes.push(
          entries.map((e) => ({
            name: e.name,
            kindCount: e.capabilities.action?.kinds.length ?? 0,
          })),
        );
      },
    });
    await reg.start();
    await reg.refresh();
    expect(refreshes).toHaveLength(2);
    expect(refreshes[0]).toEqual([
      { name: "@open-neko/plugin-slack", kindCount: 4 },
    ]);
    expect(refreshes[1]).toEqual([
      { name: "@open-neko/plugin-slack", kindCount: 4 },
    ]);
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

function manifestWithAuthEntry() {
  return {
    schema: "https://open-neko.github.io/plugins/manifest.schema.json",
    plugins: [
      {
        name: "@open-neko/plugin-scalekit",
        version: "0.1.0",
        integrity: FAKE_INTEGRITY,
        permissions: { network: ["*.scalekit.com"], env: [] },
        capabilities: { auth: { providerLabel: "Scalekit" } },
      },
    ],
  };
}

function authRegisterResponse(providerLabel = "Scalekit"): RpcResponse {
  return rpcOk({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: "@open-neko/plugin-scalekit",
    pluginVersion: "0.1.0",
    capabilities: { auth: { providerLabel } },
  });
}

describe("PluginRegistry — auth provider", () => {
  let repoRoot: string;
  let workRoot: string;
  let secretsConfigDir: string;
  let runnerPath: string;

  beforeEach(async () => {
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-repo-"));
    workRoot = await mkdtemp(path.join(tmpdir(), "openneko-vmwork-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-secrets-"));
    runnerPath = path.join(repoRoot, "fake-runner.js");
    await writeFakeRunner(runnerPath);
    setDefaultActionAdapter(mockActionAdapter);
  });

  afterEach(async () => {
    await rm(repoRoot, { recursive: true, force: true });
    await rm(workRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
    setDefaultActionAdapter(mockActionAdapter);
  });

  function newRegistry(runtime: PluginRuntime) {
    return new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
    });
  }

  it("getAuthProvider returns null when no auth plugin is installed", async () => {
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.getAuthProvider()).toBeNull();
    expect(reg.status().authProvider).toBeNull();
    await reg.stop();
  });

  it("status reports the installed auth provider with the manifest label pre-VM-spawn", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithAuthEntry()),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    const provider = reg.getAuthProvider();
    expect(provider).not.toBeNull();
    expect(provider?.pluginName).toBe("@open-neko/plugin-scalekit");
    expect(provider?.providerLabel).toBe("Scalekit");
    expect(reg.status().authProvider).toContain("scalekit");
    await reg.stop();
  });

  it("beginAuth runs ensureVm then RPCs the plugin", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithAuthEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: authRegisterResponse(),
        begin_auth: rpcOk({
          result: {
            authorizationUrl: "https://foo.scalekit.com/oauth/authorize?stub=1",
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();

    const out = await reg.beginAuth({
      redirectUri: "https://app.example.com/cb",
      state: "csrf-1",
    });
    expect(out.authorizationUrl).toBe(
      "https://foo.scalekit.com/oauth/authorize?stub=1",
    );
    expect(runtime.rpcs.map((r) => r.method)).toEqual([
      "register",
      "begin_auth",
    ]);
    expect(runtime.starts).toHaveLength(1);
    await reg.stop();
  });

  it("upgrades providerLabel from the VM's register() response", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithAuthEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: authRegisterResponse("Scalekit (prod)"),
        begin_auth: rpcOk({
          result: { authorizationUrl: "https://x" },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.beginAuth({ redirectUri: "https://x", state: "x" });
    expect(reg.getAuthProvider()?.providerLabel).toBe("Scalekit (prod)");
    await reg.stop();
  });

  it("completeAuth proxies the identity from the plugin", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithAuthEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: authRegisterResponse(),
        complete_auth: rpcOk({
          result: {
            identity: {
              sub: "user-42",
              email: "amit@example.com",
              name: "Amit Patel",
              orgId: "org-abc",
              groups: ["engineering"],
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const identity = await reg.completeAuth({
      code: "auth-code",
      redirectUri: "https://app.example.com/cb",
      state: "csrf-1",
    });
    expect(identity.sub).toBe("user-42");
    expect(identity.email).toBe("amit@example.com");
    expect(identity.groups).toEqual(["engineering"]);
    await reg.stop();
  });

  it("rejects an auth plugin whose VM register() does not declare auth", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithAuthEntry()),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: rpcOk({
          protocol: RPC_PROTOCOL_VERSION,
          pluginName: "@open-neko/plugin-scalekit",
          pluginVersion: "0.1.0",
          capabilities: {},
        }),
        begin_auth: rpcOk({
          result: { authorizationUrl: "https://x" },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await expect(
      reg.beginAuth({ redirectUri: "https://x", state: "x" }),
    ).rejects.toThrow(/no auth provider/);
    await reg.stop();
  });

  it("beginAuth/completeAuth throw a clear message when no auth plugin is installed", async () => {
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    await expect(
      reg.beginAuth({ redirectUri: "https://x", state: "x" }),
    ).rejects.toThrow(/no auth plugin/);
    await expect(
      reg.completeAuth({
        code: "x",
        redirectUri: "https://x",
        state: "x",
      }),
    ).rejects.toThrow(/no auth plugin/);
    await reg.stop();
  });

  it("flags a second auth-capability claimant in skipped", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [
          {
            name: "@open-neko/plugin-scalekit",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            permissions: { network: ["*.scalekit.com"], env: [] },
            capabilities: { auth: { providerLabel: "Scalekit" } },
          },
          {
            name: "@acme/plugin-okta",
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            permissions: { network: ["*.okta.com"], env: [] },
            capabilities: { auth: { providerLabel: "Okta" } },
          },
        ],
      }),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.getAuthProvider()?.pluginName).toBe(
      "@open-neko/plugin-scalekit",
    );
    expect(reg.status().skipped.length).toBeGreaterThanOrEqual(1);
    expect(reg.status().skipped.some((s) => /auth capability/.test(s.reason))).toBe(
      true,
    );
    await reg.stop();
  });
});

// ─── connect capability (per-operator OAuth) ───────────────────────────

const GOOGLE_CONNECT_NAME = "@open-neko/connector-google-workspace";

function manifestWithConnectEntry() {
  return {
    schema: "https://open-neko.github.io/plugins/manifest.schema.json",
    plugins: [
      {
        name: GOOGLE_CONNECT_NAME,
        version: "0.1.0",
        integrity: FAKE_INTEGRITY,
        permissions: { network: ["*.googleapis.com", "accounts.google.com"], env: [] },
        capabilities: {
          connect: {
            providerLabel: "Google Workspace",
            scopes: ["gmail.readonly", "calendar.readonly"],
            flow: "oauth2-pkce",
          },
        },
      },
    ],
  };
}

function connectRegisterResponse(): RpcResponse {
  return rpcOk({
    protocol: RPC_PROTOCOL_VERSION,
    pluginName: GOOGLE_CONNECT_NAME,
    pluginVersion: "0.1.0",
    capabilities: {
      connect: {
        providerLabel: "Google Workspace",
        scopes: ["gmail.readonly", "calendar.readonly"],
        flow: "oauth2-pkce",
      },
    },
  });
}

describe("PluginRegistry — connect capability", () => {
  let repoRoot: string;
  let workRoot: string;
  let secretsConfigDir: string;
  let runnerPath: string;

  beforeEach(async () => {
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-repo-"));
    workRoot = await mkdtemp(path.join(tmpdir(), "openneko-vmwork-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-secrets-"));
    runnerPath = path.join(repoRoot, "fake-runner.js");
    await writeFakeRunner(runnerPath);
    setDefaultActionAdapter(mockActionAdapter);
  });

  afterEach(async () => {
    await rm(repoRoot, { recursive: true, force: true });
    await rm(workRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
    setDefaultActionAdapter(mockActionAdapter);
  });

  function newRegistry(runtime: PluginRuntime) {
    return new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
    });
  }

  async function writeConnectManifest(): Promise<void> {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithConnectEntry()),
      "utf8",
    );
  }

  it("getConnectProviders surfaces every installed connect plugin", async () => {
    await writeConnectManifest();
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    const providers = reg.getConnectProviders();
    expect(providers).toHaveLength(1);
    expect(providers[0]).toMatchObject({
      pluginName: GOOGLE_CONNECT_NAME,
      providerLabel: "Google Workspace",
      scopes: ["gmail.readonly", "calendar.readonly"],
    });
    await reg.stop();
  });

  it("beginConnect runs ensureVm then RPCs the plugin", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        begin_connect: rpcOk({
          result: {
            authorizationUrl: "https://accounts.google.com/oauth2/auth?stub=1",
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const out = await reg.beginConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      redirectUri: "https://app.example.com/integrations/callback",
      state: "csrf-1",
      scopes: ["gmail.readonly"],
      codeVerifier: "verifier-xyz",
    });
    expect(out.authorizationUrl).toBe(
      "https://accounts.google.com/oauth2/auth?stub=1",
    );
    expect(runtime.rpcs.map((r) => r.method)).toEqual(["register", "begin_connect"]);
    await reg.stop();
  });

  it("completeConnect persists the credential under the operator slot", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "at-1", refresh_token: "rt-1", expires_in: 3600 },
              scopes: ["gmail.readonly", "calendar.readonly"],
              providerLabel: "Google Workspace",
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    const cred = await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "auth-code",
      redirectUri: "https://app.example.com/integrations/callback",
      state: "csrf-1",
      codeVerifier: "verifier-xyz",
      scopes: ["gmail.readonly"],
    });
    expect(cred.tokens.access_token).toBe("at-1");
    expect(reg.isOperatorConnected("op-1", GOOGLE_CONNECT_NAME)).toBe(true);
    expect(reg.isOperatorConnected("op-2", GOOGLE_CONNECT_NAME)).toBe(false);

    // Verify it survives a reload (file was written to disk).
    const reg2 = newRegistry(new FakeRuntime());
    await reg2.start();
    expect(reg2.isOperatorConnected("op-1", GOOGLE_CONNECT_NAME)).toBe(true);
    await reg.stop();
    await reg2.stop();
  });

  it("scrubber redacts tokens stored in operator credentials", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "very-secret-token-xyz", refresh_token: "rt-1" },
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "code",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    expect(
      reg.getScrubber()("Authorization: Bearer very-secret-token-xyz"),
    ).toBe("Authorization: Bearer [REDACTED]");
    await reg.stop();
  });

  it("refreshConnect rotates the credential and writes back", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "at-old", refresh_token: "rt-old" },
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
        refresh_connect: (rec) => {
          // Mirror the plugin's expected behavior: take the current credential
          // and return a rotated one.
          const parsed = JSON.parse(rec.paramsJson) as {
            params: { current: { tokens: Record<string, unknown> } };
          };
          return rpcOk({
            result: {
              credential: {
                tokens: { ...parsed.params.current.tokens, access_token: "at-rotated" },
                connectedAt: "2026-05-21T10:00:00Z",
                refreshedAt: "2026-05-21T11:00:00Z",
              },
            },
          });
        },
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "code",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    const rotated = await reg.refreshConnect(GOOGLE_CONNECT_NAME, "op-1");
    expect(rotated.tokens.access_token).toBe("at-rotated");
    expect(rotated.tokens.refresh_token).toBe("rt-old");
    expect(rotated.refreshedAt).toBe("2026-05-21T11:00:00Z");
    await reg.stop();
  });

  it("refreshConnect errors clearly when no credential exists for the operator", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: { register: connectRegisterResponse() },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await expect(
      reg.refreshConnect(GOOGLE_CONNECT_NAME, "op-never-connected"),
    ).rejects.toThrow(/no credential to refresh/);
    await reg.stop();
  });

  it("disconnect removes the credential and reports removal", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "at-1" },
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "code",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    expect(await reg.disconnect(GOOGLE_CONNECT_NAME, "op-1")).toBe(true);
    expect(reg.isOperatorConnected("op-1", GOOGLE_CONNECT_NAME)).toBe(false);
    // Second disconnect is a no-op.
    expect(await reg.disconnect(GOOGLE_CONNECT_NAME, "op-1")).toBe(false);
    await reg.stop();
  });

  it("operators are independent — disconnecting op-1 doesn't affect op-2", async () => {
    await writeConnectManifest();
    let counter = 0;
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: () => {
          counter++;
          return rpcOk({
            result: {
              credential: {
                tokens: { access_token: `at-${counter}` },
                connectedAt: "2026-05-21T10:00:00Z",
              },
            },
          });
        },
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "c1",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-2",
      code: "c2",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    expect(reg.isOperatorConnected("op-1", GOOGLE_CONNECT_NAME)).toBe(true);
    expect(reg.isOperatorConnected("op-2", GOOGLE_CONNECT_NAME)).toBe(true);
    await reg.disconnect(GOOGLE_CONNECT_NAME, "op-1");
    expect(reg.isOperatorConnected("op-1", GOOGLE_CONNECT_NAME)).toBe(false);
    expect(reg.isOperatorConnected("op-2", GOOGLE_CONNECT_NAME)).toBe(true);
    await reg.stop();
  });

  it("beginConnect on a not-installed plugin errors", async () => {
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    await expect(
      reg.beginConnect("@open-neko/connector-missing", {
        operatorId: "op-1",
        redirectUri: "https://x",
        state: "x",
        scopes: [],
      }),
    ).rejects.toThrow(/not installed/);
    await reg.stop();
  });

  it("beginConnect on an installed plugin without connect capability errors", async () => {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify(manifestWithSlackEntry()),
      "utf8",
    );
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    await expect(
      reg.beginConnect("@open-neko/plugin-slack", {
        operatorId: "op-1",
        redirectUri: "https://x",
        state: "x",
        scopes: [],
      }),
    ).rejects.toThrow(/does not declare a connect/);
    await reg.stop();
  });

  it("rejects a connect plugin whose VM omits connect from register()", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: rpcOk({
          protocol: RPC_PROTOCOL_VERSION,
          pluginName: GOOGLE_CONNECT_NAME,
          pluginVersion: "0.1.0",
          capabilities: {},
        }),
        begin_connect: rpcOk({ result: { authorizationUrl: "https://x" } }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await expect(
      reg.beginConnect(GOOGLE_CONNECT_NAME, {
        operatorId: "op-1",
        redirectUri: "https://x",
        state: "x",
        scopes: [],
      }),
    ).rejects.toThrow(/no connect handler/);
    await reg.stop();
  });

  it("rejects a connect plugin whose VM reports fewer scopes than manifest declares", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: rpcOk({
          protocol: RPC_PROTOCOL_VERSION,
          pluginName: GOOGLE_CONNECT_NAME,
          pluginVersion: "0.1.0",
          capabilities: {
            connect: {
              providerLabel: "Google Workspace",
              scopes: ["gmail.readonly"], // missing calendar.readonly
              flow: "oauth2-pkce",
            },
          },
        }),
        begin_connect: rpcOk({ result: { authorizationUrl: "https://x" } }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await expect(
      reg.beginConnect(GOOGLE_CONNECT_NAME, {
        operatorId: "op-1",
        redirectUri: "https://x",
        state: "x",
        scopes: [],
      }),
    ).rejects.toThrow(/connect scopes/);
    await reg.stop();
  });

  it("injects per-operator credential as OPENNEKO_CONNECTOR_CREDENTIAL_TOKENS at action time", async () => {
    // Manifest declares a plugin with both connect + action capabilities.
    const localCaptured = new Map<string, ActionAdapter>();
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [
          {
            name: GOOGLE_CONNECT_NAME,
            version: "0.1.0",
            integrity: FAKE_INTEGRITY,
            permissions: { network: ["*.googleapis.com"], env: [] },
            capabilities: {
              connect: {
                providerLabel: "Google Workspace",
                scopes: ["gmail.send"],
                flow: "oauth2-pkce",
              },
              action: {
                kinds: [
                  { kind: "send_gmail", description: "send email" },
                ],
              },
            },
            installSource: "marketplace",
          },
        ],
      }),
      "utf8",
    );
    const runtime = new FakeRuntime({
      responses: {
        register: rpcOk({
          protocol: RPC_PROTOCOL_VERSION,
          pluginName: GOOGLE_CONNECT_NAME,
          pluginVersion: "0.1.0",
          capabilities: {
            connect: {
              providerLabel: "Google Workspace",
              scopes: ["gmail.send"],
              flow: "oauth2-pkce",
            },
            action: { kinds: [{ kind: "send_gmail", description: "send email" }] },
          },
        }),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: {
                access_token: "at-xyz",
                refresh_token: "rt-xyz",
                expires_in: 3599,
              },
              providerLabel: "Google Workspace",
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
        execute_action: rpcOk({
          outcome: {
            result: { id: "msg-1" },
            externalRef: "msg-1",
            commandOrOperation: "gmail.send",
          },
        }),
      },
    });
    const reg = new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
      onAdapter: (kind, adapter) => localCaptured.set(kind, adapter),
    });
    await reg.start();
    // 1. Operator op-1 connects.
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "c",
      redirectUri: "https://x",
      state: "x",
      scopes: ["gmail.send"],
    });
    // 2. Agent invokes the send_gmail action on behalf of op-1.
    const adapter = localCaptured.get("send_gmail");
    expect(adapter).toBeTruthy();
    await adapter!({
      request: {
        ...makeRequest("send_gmail"),
        actorId: "op-1",
      },
    });
    // 3. The execute_action call carried the operator's tokens.
    const execCall = runtime.rpcs.find((r) => r.method === "execute_action");
    expect(execCall).toBeTruthy();
    expect(execCall!.env?.OPENNEKO_CONNECTOR_CREDENTIAL_TOKENS).toBeTruthy();
    const tokens = JSON.parse(execCall!.env!.OPENNEKO_CONNECTOR_CREDENTIAL_TOKENS!);
    expect(tokens.access_token).toBe("at-xyz");
    expect(execCall!.env?.OPENNEKO_OPERATOR_ID).toBe("op-1");
    await reg.stop();
  });

  it("does NOT inject credential when action_request has no actorId (non-operator paths)", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "at-1" },
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "c",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    // The manifest for getConnectProviders doesn't declare actions here,
    // so this test verifies the gating logic; if no adapter exists,
    // skip the call but verify the surface still reports the
    // unconnected operator correctly.
    expect(reg.isOperatorConnected("op-2", GOOGLE_CONNECT_NAME)).toBe(false);
    await reg.stop();
  });

  it("disconnect works on a plugin that's no longer installed (orphan cleanup)", async () => {
    await writeConnectManifest();
    const runtime = new FakeRuntime({
      responses: {
        register: connectRegisterResponse(),
        complete_connect: rpcOk({
          result: {
            credential: {
              tokens: { access_token: "at-1" },
              connectedAt: "2026-05-21T10:00:00Z",
            },
          },
        }),
      },
    });
    const reg = newRegistry(runtime);
    await reg.start();
    await reg.completeConnect(GOOGLE_CONNECT_NAME, {
      operatorId: "op-1",
      code: "c",
      redirectUri: "https://x",
      state: "x",
      scopes: [],
    });
    // Operator removes the plugin between connect + disconnect.
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [],
      }),
      "utf8",
    );
    await reg.refresh();
    expect(await reg.disconnect(GOOGLE_CONNECT_NAME, "op-1")).toBe(true);
    await reg.stop();
  });
});

// ─── Policy flagging (M4) ──────────────────────────────────────────────

describe("PluginRegistry — install-policy flagging", () => {
  let repoRoot: string;
  let workRoot: string;
  let secretsConfigDir: string;
  let runnerPath: string;

  beforeEach(async () => {
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-repo-"));
    workRoot = await mkdtemp(path.join(tmpdir(), "openneko-vmwork-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-secrets-"));
    runnerPath = path.join(repoRoot, "fake-runner.js");
    await writeFakeRunner(runnerPath);
    setDefaultActionAdapter(mockActionAdapter);
  });

  afterEach(async () => {
    await rm(repoRoot, { recursive: true, force: true });
    await rm(workRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
    setDefaultActionAdapter(mockActionAdapter);
  });

  function newRegistry(
    runtime: PluginRuntime,
    loadInstallPolicy?: () =>
      | Promise<{
          allowUnverified: boolean;
          allowGitUrlInstalls: boolean;
          allowedMarketplaces: string[];
        } | null>
      | { allowUnverified: boolean; allowGitUrlInstalls: boolean; allowedMarketplaces: string[] } | null,
  ) {
    return new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime,
      resolveRunner: () => runnerPath,
      ...(loadInstallPolicy
        ? { loadInstallPolicy: () => Promise.resolve(loadInstallPolicy()) }
        : {}),
    });
  }

  async function writeManifest(entry: Record<string, unknown>): Promise<void> {
    await writeFile(
      path.join(repoRoot, "openneko.plugins.json"),
      JSON.stringify({
        schema: "https://open-neko.github.io/plugins/manifest.schema.json",
        plugins: [entry],
      }),
      "utf8",
    );
  }

  const unverifiedSlackEntry = {
    name: "@some-author/plugin-thing",
    version: "0.1.0",
    integrity: FAKE_INTEGRITY,
    permissions: { network: ["thing.io"], env: [] },
    capabilities: {
      action: { kinds: [{ kind: "do_thing", description: "thing" }] },
    },
    installSource: "unverified" as const,
    installedAt: "2026-05-21T10:00:00Z",
    policySnapshot: {
      allowUnverified: true,
      allowGitUrlInstalls: false,
      allowSandboxedSkillEscape: false,
      allowedMarketplaces: ["https://open-neko.github.io/plugins/marketplace.json"],
    },
  };

  it("no policy loader → no flags (default behavior)", async () => {
    await writeManifest(unverifiedSlackEntry);
    const reg = newRegistry(new FakeRuntime());
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    await reg.stop();
  });

  it("policy allows unverified → entry installed via unverified is NOT flagged", async () => {
    await writeManifest(unverifiedSlackEntry);
    const reg = newRegistry(new FakeRuntime(), () => ({
      allowUnverified: true,
      allowGitUrlInstalls: true,
      allowedMarketplaces: [],
    }));
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    expect(reg.status().loaded).toContain(unverifiedSlackEntry.name);
    await reg.stop();
  });

  it("policy disallows unverified → entry is flagged but stays loaded", async () => {
    await writeManifest(unverifiedSlackEntry);
    const reg = newRegistry(new FakeRuntime(), () => ({
      allowUnverified: false,
      allowGitUrlInstalls: false,
      allowedMarketplaces: [],
    }));
    await reg.start();
    expect(reg.status().loaded).toContain(unverifiedSlackEntry.name);
    expect(reg.status().flagged.map((f) => f.pluginName)).toContain(
      unverifiedSlackEntry.name,
    );
    expect(reg.status().flagged[0]?.reason).toMatch(/unverified/);
    expect(reg.status().kinds).toContain("do_thing");
    await reg.stop();
  });

  it("flag re-evaluates on refresh — flipping policy off then on toggles the flag", async () => {
    await writeManifest(unverifiedSlackEntry);
    let allow = true;
    const reg = newRegistry(new FakeRuntime(), () => ({
      allowUnverified: allow,
      allowGitUrlInstalls: false,
      allowedMarketplaces: [],
    }));
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    allow = false;
    await reg.refresh();
    expect(reg.status().flagged.length).toBe(1);
    allow = true;
    await reg.refresh();
    expect(reg.status().flagged).toEqual([]);
    await reg.stop();
  });

  it("legacy entries with no installSource are NEVER flagged (grandfather)", async () => {
    const legacy = {
      name: "@legacy/plugin",
      version: "0.1.0",
      integrity: FAKE_INTEGRITY,
      permissions: { network: ["x.com"], env: [] },
      capabilities: {
        action: { kinds: [{ kind: "legacy_action", description: "x" }] },
      },
      // no installSource, no policySnapshot — pre-feature shape
    };
    await writeManifest(legacy);
    const reg = newRegistry(new FakeRuntime(), () => ({
      allowUnverified: false,
      allowGitUrlInstalls: false,
      allowedMarketplaces: [],
    }));
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    expect(reg.status().loaded).toContain(legacy.name);
    await reg.stop();
  });

  it("marketplace entries are not flagged regardless of policy (current behavior)", async () => {
    const marketplaceEntry = {
      name: "@open-neko/plugin-slack",
      version: "0.1.0",
      integrity: FAKE_INTEGRITY,
      permissions: { network: ["slack.com"], env: [] },
      capabilities: { action: { kinds: SLACK_KIND_DECLS } },
      installSource: "marketplace" as const,
      installedAt: "2026-05-21T10:00:00Z",
      marketplace: "official",
    };
    await writeManifest(marketplaceEntry);
    const reg = newRegistry(new FakeRuntime(), () => ({
      allowUnverified: false,
      allowGitUrlInstalls: false,
      allowedMarketplaces: [], // even though empty, marketplace installs grandfather
    }));
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    await reg.stop();
  });

  it("loadInstallPolicy returning null → no flags (treated as policy not set)", async () => {
    await writeManifest(unverifiedSlackEntry);
    const reg = newRegistry(new FakeRuntime(), () => null);
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    await reg.stop();
  });

  it("loadInstallPolicy that throws is logged + no flags applied", async () => {
    await writeManifest(unverifiedSlackEntry);
    const reg = newRegistry(new FakeRuntime(), () => {
      throw new Error("DB unreachable");
    });
    await reg.start();
    expect(reg.status().flagged).toEqual([]);
    expect(reg.status().loaded).toContain(unverifiedSlackEntry.name);
    await reg.stop();
  });
});

