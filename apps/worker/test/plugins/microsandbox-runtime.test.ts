import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  MicrosandboxRuntime,
  networkModeFor,
  type MicrosandboxFactory,
  type NetworkPolicyApi,
} from "../../src/plugins/microsandbox-runtime";
import type {
  MicrosandboxBuilder,
  MicrosandboxExecOutput,
  MicrosandboxInstance,
} from "../../src/plugins/microsandbox-sdk";

interface RecordedExec {
  cmd: string;
  args: string[];
}

interface FakeSandboxControls {
  execResult: { code: number; stdout: string; stderr: string };
  execDelayMs: number;
  stopped: boolean;
}

interface FakeBuilderRecording {
  name: string;
  image?: string;
  cpus?: number;
  memoryMb?: number;
  replaced?: boolean;
  network?: "none" | "publicOnly" | "allowAll";
  volumes: Array<{ path: string; host: string }>;
}

function makeFakeFactoryAndPolicy(): {
  factory: MicrosandboxFactory;
  policy: NetworkPolicyApi;
  state: {
    builders: FakeBuilderRecording[];
    instances: Map<string, { controls: FakeSandboxControls; execs: RecordedExec[] }>;
  };
} {
  const state = {
    builders: [] as FakeBuilderRecording[],
    instances: new Map<
      string,
      { controls: FakeSandboxControls; execs: RecordedExec[] }
    >(),
  };

  const policy: NetworkPolicyApi = {
    none: () => ({ __policy: "none" }),
    publicOnly: () => ({ __policy: "publicOnly" }),
    allowAll: () => ({ __policy: "allowAll" }),
  };

  const factory: MicrosandboxFactory = {
    builder(name: string) {
      const recording: FakeBuilderRecording = { name, volumes: [] };
      state.builders.push(recording);

      const builder: MicrosandboxBuilder = {
        image(v) {
          recording.image = v;
          return builder;
        },
        cpus(v) {
          recording.cpus = v;
          return builder;
        },
        memory(v) {
          recording.memoryMb = v;
          return builder;
        },
        replace() {
          recording.replaced = true;
          return builder;
        },
        network(configure) {
          configure({
            policy(p) {
              recording.network = (p as { __policy: FakeBuilderRecording["network"] }).__policy;
              return p;
            },
          });
          return builder;
        },
        volume(p, configure) {
          configure({
            bind(host) {
              recording.volumes.push({ path: p, host });
              return host;
            },
          });
          return builder;
        },
        async create(): Promise<MicrosandboxInstance> {
          const controls: FakeSandboxControls = {
            execResult: { code: 0, stdout: "{}", stderr: "" },
            execDelayMs: 0,
            stopped: false,
          };
          const execs: RecordedExec[] = [];
          state.instances.set(name, { controls, execs });

          const instance: MicrosandboxInstance = {
            async exec(cmd, args): Promise<MicrosandboxExecOutput> {
              execs.push({ cmd, args });
              if (controls.execDelayMs > 0) {
                await new Promise((r) => setTimeout(r, controls.execDelayMs));
              }
              const r = controls.execResult;
              return {
                code: r.code,
                stdout: () => r.stdout,
                stderr: () => r.stderr,
              };
            },
            async stopAndWait() {
              controls.stopped = true;
            },
          };
          return instance;
        },
      };
      return builder;
    },
  };

  return { factory, policy, state };
}

describe("MicrosandboxRuntime", () => {
  let workdir: string;
  let runtime: MicrosandboxRuntime;
  let factory: ReturnType<typeof makeFakeFactoryAndPolicy>;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(tmpdir(), "msbx-rt-"));
    factory = makeFakeFactoryAndPolicy();
    runtime = new MicrosandboxRuntime({
      image: "node:20-alpine",
      cpus: 1,
      memoryMb: 256,
      sandboxFactory: factory.factory,
      networkPolicy: factory.policy,
      onLog: () => {},
    });
  });

  afterEach(async () => {
    await runtime.destroyAll();
    await rm(workdir, { recursive: true, force: true });
  });

  it("start() configures the builder with image, cpus, memory, replace, volume, network", async () => {
    await runtime.start({
      id: "plugin-a",
      hostWorkspacePath: path.join(workdir, "a"),
      network: "none",
    });
    expect(factory.state.builders).toHaveLength(1);
    const b = factory.state.builders[0]!;
    expect(b.name).toBe("plugin-a");
    expect(b.image).toBe("node:20-alpine");
    expect(b.cpus).toBe(1);
    expect(b.memoryMb).toBe(256);
    expect(b.replaced).toBe(true);
    expect(b.network).toBe("none");
    expect(b.volumes).toEqual([
      { path: "/workspace", host: path.join(workdir, "a") },
    ]);
    expect(runtime.hasPlugin("plugin-a")).toBe(true);
  });

  it("start() with network=public translates to NetworkPolicy.publicOnly", async () => {
    await runtime.start({
      id: "plugin-b",
      hostWorkspacePath: path.join(workdir, "b"),
      network: "public",
    });
    expect(factory.state.builders[0]?.network).toBe("publicOnly");
  });

  it("start() is idempotent — second call for the same id is a no-op", async () => {
    const spec = {
      id: "plugin-a",
      hostWorkspacePath: path.join(workdir, "a"),
      network: "none" as const,
    };
    await runtime.start(spec);
    await runtime.start(spec);
    expect(factory.state.builders).toHaveLength(1);
  });

  it("callRpc() execs node /workspace/run.js with method and params", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    const inst = factory.state.instances.get("p")!;
    inst.controls.execResult = {
      code: 0,
      stdout: JSON.stringify({ ok: true, result: { x: 1 } }),
      stderr: "",
    };
    const response = await runtime.callRpc("p", "register", JSON.stringify({}));
    expect(response.ok).toBe(true);
    if (!response.ok) return;
    expect(response.result).toEqual({ x: 1 });
    expect(inst.execs).toEqual([
      {
        cmd: "node",
        args: ["/workspace/run.js", "register", "{}"],
      },
    ]);
  });

  it("callRpc() ignores non-JSON log lines preceding the JSON response", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    const inst = factory.state.instances.get("p")!;
    inst.controls.execResult = {
      code: 0,
      stdout: `loading...\nimported deps\n${JSON.stringify({ ok: true, result: 42 })}`,
      stderr: "",
    };
    const response = await runtime.callRpc("p", "register", "{}");
    expect(response.ok).toBe(true);
    if (!response.ok) return;
    expect(response.result).toBe(42);
  });

  it("callRpc() throws if exec produces empty stdout and non-zero exit", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    factory.state.instances.get("p")!.controls.execResult = {
      code: 1,
      stdout: "",
      stderr: "boom",
    };
    await expect(runtime.callRpc("p", "register", "{}")).rejects.toThrow(
      /failed before producing JSON/,
    );
  });

  it("callRpc() throws on non-JSON last line", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    factory.state.instances.get("p")!.controls.execResult = {
      code: 0,
      stdout: "not json at all",
      stderr: "",
    };
    await expect(runtime.callRpc("p", "register", "{}")).rejects.toThrow(
      /non-JSON stdout/,
    );
  });

  it("callRpc() throws on JSON that doesn't match RpcResponse shape", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    factory.state.instances.get("p")!.controls.execResult = {
      code: 0,
      stdout: JSON.stringify({ totally: "wrong" }),
      stderr: "",
    };
    await expect(runtime.callRpc("p", "register", "{}")).rejects.toThrow(
      /non-RpcResponse shape/,
    );
  });

  it("callRpc() respects the per-call timeout", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    factory.state.instances.get("p")!.controls.execDelayMs = 1_000;
    await expect(
      runtime.callRpc("p", "register", "{}", { timeoutMs: 50 }),
    ).rejects.toThrow(/timed out after 50ms/);
  });

  it("callRpc() throws when the plugin id has not been started", async () => {
    await expect(runtime.callRpc("missing", "register", "{}")).rejects.toThrow(
      /plugin VM not started: missing/,
    );
  });

  it("stop() removes the VM and calls stopAndWait on the instance", async () => {
    await runtime.start({
      id: "p",
      hostWorkspacePath: path.join(workdir, "p"),
      network: "none",
    });
    const inst = factory.state.instances.get("p")!;
    await runtime.stop("p");
    expect(inst.controls.stopped).toBe(true);
    expect(runtime.hasPlugin("p")).toBe(false);
  });

  it("destroyAll() stops every running VM", async () => {
    await runtime.start({
      id: "a",
      hostWorkspacePath: path.join(workdir, "a"),
      network: "none",
    });
    await runtime.start({
      id: "b",
      hostWorkspacePath: path.join(workdir, "b"),
      network: "public",
    });
    await runtime.destroyAll();
    expect(factory.state.instances.get("a")?.controls.stopped).toBe(true);
    expect(factory.state.instances.get("b")?.controls.stopped).toBe(true);
    expect(runtime.hasPlugin("a")).toBe(false);
    expect(runtime.hasPlugin("b")).toBe(false);
  });

  it("throws a clear error if no factory is injected", async () => {
    const r = new MicrosandboxRuntime({
      image: "node:20-alpine",
      cpus: 1,
      memoryMb: 256,
      networkPolicy: factory.policy,
      onLog: () => {},
    });
    await expect(
      r.start({
        id: "x",
        hostWorkspacePath: path.join(workdir, "x"),
        network: "none",
      }),
    ).rejects.toThrow(/no sandboxFactory/);
  });

  it("throws a clear error if no networkPolicy is injected", async () => {
    const r = new MicrosandboxRuntime({
      image: "node:20-alpine",
      cpus: 1,
      memoryMb: 256,
      sandboxFactory: factory.factory,
      onLog: () => {},
    });
    await expect(
      r.start({
        id: "x",
        hostWorkspacePath: path.join(workdir, "x"),
        network: "none",
      }),
    ).rejects.toThrow(/no networkPolicy/);
  });
});

describe("networkModeFor", () => {
  it("returns 'none' for empty host list", () => {
    expect(networkModeFor([])).toBe("none");
  });

  it("returns 'public' for any declared host", () => {
    expect(networkModeFor(["api.example.com"])).toBe("public");
    expect(networkModeFor(["a.b.c", "*.example.org"])).toBe("public");
  });
});

// Silences the unused-var warning when vitest tree-shakes; vi is imported
// only to keep the test module shape consistent with sibling tests.
void vi;
