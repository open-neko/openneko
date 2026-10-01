import {
  access,
  cp,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { Readable, Writable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import type { RunAgentBackendInput } from "../src/work/agent-core";
import { GRAPHJIN_DIRECT_GOVERNED_POLICY } from "../src/work/graphjin-tool-policy";
import { KNOWLEDGE_FILES, refreshKnowledgeSnapshot } from "../src/knowledge-cache";
import type { RunWorkflowAgentBackendInput } from "../src/workflows/agent-core";
import { parseHarnessRouting } from "../src/work/harness-routing";

/**
 * The launcher shells out to the `openshell` CLI. We mock spawn: non-exec calls
 * (create/upload/delete) return empty stdout + exit 0; the `exec` call returns
 * a stdout stream carrying tagged EVENT + RESULT lines, which the launcher must
 * relay to `emit` and parse into the AgentRunResult.
 */
const h = vi.hoisted(() => {
  const calls: { args: string[]; stdin?: string; env?: Record<string,string> }[] = [];
  const state = {
    inspections: [] as Array<string>,
    operations: [] as Array<{id:number;instruction:string;result:unknown}>,
    inventory: "[]",
    holdExec: false,
    failPolicy: false,
    failReconcile: false,
    deleteMissing: false,
    collideOnNextCreate: false,
    execLines: undefined as string[] | undefined,
  };
  function spawn(_cmd: string, args: string[], options?: {env?: Record<string,string>}) {
    const call: { args: string[]; stdin?: string; env?: Record<string,string> } = { args,
      env: options?.env?.HARNESS_MODEL_ROUTES ? {HARNESS_MODEL_ROUTES:options.env.HARNESS_MODEL_ROUTES} : undefined };
    calls.push(call);
    const inspector = args.includes("/usr/local/bin/harness-inspect") || _cmd.endsWith("harness-inspect");
    const inspection = inspector ? state.inspections.shift() : undefined;
    const inspectionFailed = inspection === "busy";
    const isExec = args.includes("exec");
    const warmCreate = args.includes("create") && args.includes("/app/hermes-warm.py");
    const failedPolicy = args.includes("set") && state.failPolicy;
    const missingDelete = args.includes("delete") && state.deleteMissing;
    const createCollision =
      args.includes("create") && state.collideOnNextCreate;
    if (createCollision) state.collideOnNextCreate = false;
    const reg = (store: Record<string, Array<(...a: unknown[]) => void>>) =>
      (ev: string, cb: (...a: unknown[]) => void) => {
        (store[ev] ??= []).push(cb);
      };
    const fire = (
      store: Record<string, Array<(...a: unknown[]) => void>>,
      ev: string,
      ...a: unknown[]
    ) => (store[ev] ?? []).forEach((cb) => cb(...a));
    const ch: Record<string, Array<(...a: unknown[]) => void>> = {};
    const stderr = Readable.from(
      missingDelete ? ["sandbox not found"] : createCollision
        ? ["Error: × sandbox 'work-run-1' already exists\n"]
        : [],
    );
    const reconciliation = args.findIndex(arg => arg.startsWith("exec(__import__('base64')"));
    const lines = (inspector && inspection !== undefined ? [inspection] : args.includes("list") ? [state.inventory] : warmCreate ? ["__openneko_warm_ready__\n"] : failedPolicy ? ["policy submitted\n"] : isExec
      ? state.execLines ?? [
          'noise before\n',
          `\n__openneko_event__${JSON.stringify({ type: "message", role: "assistant", content: "hi" })}\n`,
          `\n__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "hi there", backendState: { t: 1 } })}\n`,
        ]
      : []);
    let closed = false;
    const closeOnce = () => {
      if (closed) return;
      closed = true;
      fire(ch, "close", createCollision || failedPolicy || missingDelete || inspectionFailed ? 1 : 0);
    };
    const stdout = reconciliation < 0 ? Readable.from(lines) : new Readable({ read() {} });
    const stdin = new Writable({
      write(chunk, _encoding, done) { call.stdin = (call.stdin ?? "") + chunk.toString(); done(); },
      final(done) {
        if (reconciliation >= 0) {
          const response = state.failReconcile ? "invalid reconciliation" : "__openneko_sync__" + JSON.stringify(JSON.parse(call.stdin!).directories.map((directory: { entries: Record<string, unknown> }) => Object.entries(directory.entries).filter(([, value]) => value !== null).map(([key]) => key)));
          stdout.push(response + "\n"); stdout.push(null);
        }
        done();
      },
    });
    if (!warmCreate && (!isExec || !state.holdExec)) {
      stdout.on("end", () => queueMicrotask(closeOnce));
    }
    return {
      stdin,
      stdout,
      stderr,
      on: reg(ch),
      once: reg(ch),
      kill() {
        closeOnce();
        return true;
      },
    };
  }
  return { calls, state, spawn };
});

vi.mock("node:child_process", () => ({ spawn: h.spawn }));
vi.mock("../src/work/harness-operation", () => ({loadHarnessOperations: async () => h.state.operations}));
vi.mock("../src/work/harness-run-journal", () => ({
  withHarnessRunJournal: (_scope: unknown, run: (signal: AbortSignal) => Promise<unknown>) => run(new AbortController().signal),
}));
vi.mock("../src/work/harness-launch-journal", async importOriginal => ({
  ...await importOriginal<typeof import("../src/work/harness-launch-journal")>(),
  withHarnessLaunchLock: (_root: string, run: (signal: AbortSignal) => Promise<unknown>) => run(new AbortController().signal),
}));

// Capture the job descriptor the launcher writes (then uploads to the box), so
// we can assert exactly what crosses the host→sandbox boundary.
const jobCapture = vi.hoisted(() => ({
  jobs: [] as Array<Record<string, unknown>>,
  policies: [] as Array<ReturnType<typeof JSON.parse>>,
  hermesEnvs: [] as string[],
}));
vi.mock("node:fs/promises", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:fs/promises")>();
  return {
    ...actual,
    writeFile: async (p: unknown, data: unknown, ...rest: unknown[]) => {
      if (typeof p === "string" && p.endsWith("job.json")) {
        try {
          jobCapture.jobs.push(JSON.parse(String(data)));
        } catch {
          /* ignore non-JSON writes */
        }
      }
      if (typeof p === "string" && p.endsWith("policy.json")) {
        try {
          jobCapture.policies.push(JSON.parse(String(data)));
        } catch {
          /* ignore non-JSON writes */
        }
      }
      if (
        typeof p === "string" &&
        p.endsWith("hermes-home/.env")
      ) {
        jobCapture.hermesEnvs.push(String(data));
      }
      return (actual.writeFile as (...a: unknown[]) => Promise<void>)(p, data, ...rest);
    },
  };
});

const {
  makeSandboxRunCore,
  prepareSandboxCapacity,
  closeSandboxPools,
  makeSandboxJobRunCore,
  makeSandboxWorkflowRunCore,
  isMissingSandboxDelete,
  buildModelEgressArgs,
  buildSandboxPolicy,
  buildScopedEgressArgs,
  deleteOpenShellProvider,
  ensureOpenShellProvider,
  sandboxLauncherOptionsFromConfig,
  sandboxLauncherOptionsFromEnv,
  stageSandboxWorkspace,
  verifyOpenShellGateway,
  workflowExecBudgetMs,
} = await import("../src/work/sandbox-launcher");

it("accepts only a missing sandbox as completed-run cleanup", () => {
  expect(isMissingSandboxDelete(new Error("openshell sandbox delete h-1 exited 1; stderr=sandbox not found"))).toBe(true);
  expect(isMissingSandboxDelete(new Error("openshell sandbox delete h-1 exited 1; stderr=sandbox 'h-1' does not exist"))).toBe(true);
  expect(isMissingSandboxDelete(new Error("openshell sandbox delete h-1 exited 1; stderr=gateway server not found"))).toBe(false);
  expect(isMissingSandboxDelete(new Error("openshell sandbox delete h-1 timed out after 60000ms"))).toBe(false);
});

describe("sandboxLauncherOptionsFromEnv", () => {
  it("ignores a persisted operator binary and exposes only model hosts", () => {
    vi.stubEnv("OPENNEKO_AGENT_MODEL_HOST", "models.example.com,models.dev");
    vi.stubEnv(
      "OPENNEKO_AGENT_MODEL_BINARY",
      "/usr/local/uv/python/cpython-3.11.16-linux-x86_64-gnu/bin/python3.11",
    );
    try {
      const options = sandboxLauncherOptionsFromEnv();
      expect(options.modelHosts).toEqual([
        { host: "models.example.com" },
        { host: "models.dev" },
      ]);
      expect(options).not.toHaveProperty("modelEgress");
    } finally {
      vi.unstubAllEnvs();
    }
  });
});

describe("sandboxLauncherOptionsFromConfig", () => {
  it("defaults to one warm slot and allows explicitly disabling it", () => {
    vi.stubEnv("OPENNEKO_AGENT_WARM_POOL_SIZE", undefined);
    try {
      expect(sandboxLauncherOptionsFromConfig({}).warmPoolSize).toBe(1);
      vi.stubEnv("OPENNEKO_AGENT_WARM_POOL_SIZE", "0");
      expect(sandboxLauncherOptionsFromConfig({}).warmPoolSize).toBe(0);
    } finally { vi.unstubAllEnvs(); }
  });

  it("forwards resource limits from the host environment", () => {
    vi.stubEnv("OPENNEKO_AGENT_CPUS", "500m");
    vi.stubEnv("OPENNEKO_AGENT_MEMORY", "2Gi");
    try {
      expect(sandboxLauncherOptionsFromConfig({})).toMatchObject({ cpu: "500m", memory: "2Gi" });
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("uses the run-local provider snapshot instead of stale process globals", () => {
    vi.stubEnv(
      "OPENNEKO_AGENT_MODEL_HOST",
      "generativelanguage.googleapis.com,models.dev",
    );
    vi.stubEnv("OPENNEKO_AGENT_MODEL_KEY_ENV", "GEMINI_API_KEY");
    vi.stubEnv("OPENNEKO_AGENT_MODEL_PROVIDER", "stale-gemini-provider");
    vi.stubEnv("OPENNEKO_AGENT_HERMES_HOME", "/tmp/stale-gemini-home");
    try {
      const options = sandboxLauncherOptionsFromConfig({
        modelProvider: "openneko-agent",
        modelHosts: [
          { host: "api.anthropic.com" },
          { host: "models.dev" },
        ],
        keyAliases: [{ from: "api_key", to: "ANTHROPIC_API_KEY" }],
        hermesHomeHostPath: "/tmp/current-anthropic-home",
      });

      expect(options).toMatchObject({
        modelProvider: "openneko-agent",
        modelHosts: [
          { host: "api.anthropic.com" },
          { host: "models.dev" },
        ],
        keyAliases: [{ from: "api_key", to: "ANTHROPIC_API_KEY" }],
        hermesHomeHostPath: "/tmp/current-anthropic-home",
      });
    } finally {
      vi.unstubAllEnvs();
    }
  });
});

function fakeInput(
  emit: (e: AgentEvent) => Promise<void>,
  backend?: RunAgentBackendInput["backend"],
): RunAgentBackendInput {
  return {
    backend:
      backend ??
      ({ id: "hermes", capabilities: { mcpTools: false } } as RunAgentBackendInput["backend"]),
    prompt: "PROMPT",
    userMessage: "hello",
    orgId: "org-1",
    threadId: "thr-1",
    runId: "run-1",
    workspace: fullWorkspace("/tmp/ws/org-1"),
    backendState: undefined,
    pluginActions: [],
    emit,
  };
}

function fakeWorkflowInput(
  emit: (e: AgentEvent) => Promise<void>,
  backend?: RunWorkflowAgentBackendInput["backend"],
  workspaceOverride?: AgentWorkspace,
): RunWorkflowAgentBackendInput {
  return {
    backend:
      backend ??
      ({ id: "hermes", capabilities: { mcpTools: false } } as RunWorkflowAgentBackendInput["backend"]),
    prompt: "WORKFLOW PROMPT",
    userMessage: "begin",
    orgId: "org-1",
    threadId: "thr-1",
    runId: "run-1",
    workflowRunId: "workflow-run-1",
    mode: "headless",
    networkHosts: [],
    triggeredByObservationId: "obs-1",
    workspace:
      workspaceOverride ??
      fullWorkspace("/tmp/ws/org-1"),
    emit,
  };
}

function fakeJobInput(
  workspace: AgentWorkspace,
  access: {
    graphjinRead?: boolean;
    graphjinAgent?: boolean;
    memorySearch?: boolean;
  } = {},
) {
  return {
    backend: {
      id: "hermes",
      capabilities: { mcpTools: true },
    } as RunAgentBackendInput["backend"],
    orgId: "org-1",
    runId: "job-1",
    workspace,
    run: {
      prompt: "JOB PROMPT",
      timeoutMs: 45_000,
      retries: 0,
      tag: "profile-job",
      workspace,
    },
    access,
    emit: async () => {},
  };
}

function fullWorkspace(orgRoot: string): AgentWorkspace {
  return {
    orgRoot,
    skillsRoot: join(orgRoot, "skills"),
    memoryRoot: join(orgRoot, "memory"),
    knowledgeRoot: join(orgRoot, "knowledge"),
    uploadsRoot: join(orgRoot, "uploads"),
    runsRoot: join(orgRoot, "runs"),
    threadUploadsRoot: join(orgRoot, "uploads", "thr-1"),
    runRoot: join(orgRoot, "runs", "run-1"),
    artifactRoot: join(orgRoot, "runs", "run-1", "artifacts"),
    binRoot: join(orgRoot, "runs", "run-1", "bin"),
  };
}

describe("buildModelEgressArgs", () => {
  it("returns null with no egress", () => {
    expect(buildModelEgressArgs("s", [])).toBeNull();
  });
  it("emits per-host endpoints + a binary scope + all-path allows", () => {
    expect(
      buildModelEgressArgs("s", [
        { host: "generativelanguage.googleapis.com", binary: "/usr/local/uv/python/x/bin/python3.11" },
      ]),
    ).toEqual([
      "policy",
      "update",
      "s",
      "--add-endpoint",
      "generativelanguage.googleapis.com:443:read-write:rest:enforce",
      "--binary",
      "/usr/local/uv/python/x/bin/python3.11",
      "--add-allow",
      "generativelanguage.googleapis.com:443:*:/**",
      "--wait",
      "--timeout",
      "60",
    ]);
  });
  it("honours an explicit non-443 port (the broker channel)", () => {
    expect(
      buildModelEgressArgs("s", [
        { host: "host.openshell.internal", binary: "/usr/local/bin/node", port: 4199 },
      ]),
    ).toEqual([
      "policy",
      "update",
      "s",
      "--add-endpoint",
      "host.openshell.internal:4199:read-write:rest:enforce",
      "--binary",
      "/usr/local/bin/node",
      "--add-allow",
      "host.openshell.internal:4199:*:/**",
      "--wait",
      "--timeout",
      "60",
    ]);
  });

  it("keeps endpoint allowlists separated by executable", () => {
    const updates = buildScopedEgressArgs("s", [
      { host: "models.example.com", binary: "/bin/model" },
      { host: "graphjin.internal", binary: "/bin/graphjin", port: 8080 },
      { host: "broker.internal", binary: "/bin/node", port: 4199 },
    ]);

    expect(updates).toHaveLength(3);
    const model = updates.find((args) => args.includes("/bin/model"))!;
    expect(model.join(" ")).toContain("models.example.com:443");
    expect(model.join(" ")).not.toContain("graphjin.internal");
    expect(model.join(" ")).not.toContain("broker.internal");
  });
});

describe("buildSandboxPolicy", () => {
  it("builds the complete creation policy with executable-scoped endpoints", () => {
    const policy = buildSandboxPolicy([
      { host: "models.example.com", binary: "/bin/model" },
      { host: "models.example.com", binary: "/bin/model" },
      { host: "graphjin.internal", binary: "/bin/graphjin", port: 8080 },
    ]);

    expect(policy.filesystem_policy.read_write).toContain("/sandbox");
    const entries = Object.values(policy.network_policies);
    expect(entries).toHaveLength(2);
    const model = entries.find((entry) => entry.binaries[0]?.path === "/bin/model")!;
    expect(model.endpoints).toHaveLength(1);
    expect(model.endpoints[0]).toMatchObject({
      host: "models.example.com",
      port: 443,
      protocol: "rest",
      enforcement: "enforce",
    });
    expect(model.endpoints.some((endpoint) => endpoint.host === "graphjin.internal"))
      .toBe(false);
  });
});

describe("stageSandboxWorkspace", () => {
  it("materializes one cached snapshot without old files or cache metadata", async () => {
    const root = await mkdtemp(join(tmpdir(), "knowledge-stage-test-"));
    try {
      const workspace = fullWorkspace(join(root, "org"));
      await refreshKnowledgeSnapshot({ root: workspace.knowledgeRoot, source: "source", mode: "agentic",
        revision: async () => "r1", build: async dir => {
          await Promise.all(KNOWLEDGE_FILES.map(file => writeFile(join(dir, file), file === "INDEX.md" ? "index" : '{"current":true}')));
          return { ok: true, files: [] };
        },
      });
      await writeFile(join(workspace.knowledgeRoot, "tables.json"), '{"obsolete":true}');
      const staged = await stageSandboxWorkspace(workspace, join(root, "stage"));
      expect((await readdir(staged.workspace.knowledgeRoot)).sort()).toEqual([...KNOWLEDGE_FILES].sort());
      expect(await readFile(join(staged.workspace.knowledgeRoot, "tables.json"), "utf8")).toBe('{"current":true}');
    } finally { await rm(root, { recursive: true, force: true }); }
  });
  it("exposes only the current run, current thread uploads, knowledge, and skill overrides", async () => {
    const root = await mkdtemp(join(tmpdir(), "sandbox-stage-source-"));
    const stage = await mkdtemp(join(tmpdir(), "sandbox-stage-dest-"));
    try {
      const workspace = fullWorkspace(root);
      await Promise.all([
        mkdir(workspace.knowledgeRoot, { recursive: true }),
        mkdir(workspace.threadUploadsRoot, { recursive: true }),
        mkdir(workspace.runRoot, { recursive: true }),
        mkdir(join(workspace.uploadsRoot, "other-thread"), { recursive: true }),
        mkdir(join(workspace.runsRoot, "other-run"), { recursive: true }),
        mkdir(workspace.memoryRoot, { recursive: true }),
        mkdir(join(workspace.skillsRoot, "quarterly-review"), { recursive: true }),
      ]);
      await cp(
        join(process.cwd(), "assets", "builtin-skills", "records"),
        join(workspace.skillsRoot, "records"),
        { recursive: true },
      );
      await Promise.all([
        writeFile(join(workspace.knowledgeRoot, "schema.json"), "{}"),
        writeFile(join(workspace.threadUploadsRoot, "current.csv"), "current"),
        writeFile(join(workspace.runRoot, "current.txt"), "current"),
        writeFile(join(workspace.uploadsRoot, "other-thread", "private.txt"), "private"),
        writeFile(join(workspace.runsRoot, "other-run", "secret.txt"), "secret"),
        writeFile(join(workspace.memoryRoot, "member-memory.txt"), "private memory"),
        writeFile(
          join(workspace.skillsRoot, "quarterly-review", "SKILL.md"),
          "---\nname: quarterly-review\ndescription: Review quarters\n---\n",
        ),
        // Simulate a durable workspace left behind by an older release.
        writeFile(
          join(workspace.skillsRoot, "records", "SKILL.md"),
          "---\nname: records\ndescription: stale system skill\n---\n",
        ),
      ]);

      const staged = await stageSandboxWorkspace(workspace, stage, {
        requiredSkillNames: ["records"],
      });
      expect(staged.skillOverrides).toEqual(["quarterly-review", "records"]);
      await expect(
        access(join(staged.workspace.skillsRoot, "records", "SKILL.md")),
      ).resolves.toBeUndefined();
      expect(
        await readFile(join(staged.workspace.skillsRoot, "records", "SKILL.md"), "utf8"),
      ).toContain("objective urgency cannot be determined");
      expect(await readFile(join(staged.workspace.knowledgeRoot, "schema.json"), "utf8"))
        .toBe("{}");
      expect(await readFile(join(staged.workspace.threadUploadsRoot, "current.csv"), "utf8"))
        .toBe("current");
      expect(await readFile(join(staged.workspace.runRoot, "current.txt"), "utf8"))
        .toBe("current");
      await expect(
        access(join(staged.workspace.uploadsRoot, "other-thread", "private.txt")),
      ).rejects.toThrow();
      await expect(
        access(join(staged.workspace.runsRoot, "other-run", "secret.txt")),
      ).rejects.toThrow();
      await expect(
        access(join(staged.workspace.memoryRoot, "member-memory.txt")),
      ).rejects.toThrow();
    } finally {
      await Promise.all([
        rm(root, { recursive: true, force: true }),
        rm(stage, { recursive: true, force: true }),
      ]);
    }
  });
});

describe("makeSandboxRunCore", () => {
  beforeEach(() => {
    h.calls.length = 0;
    h.state.inspections = [];
    h.state.operations = [];
    h.state.inventory = "[]";
    h.state.holdExec = false;
    h.state.failPolicy = false;
    h.state.failReconcile = false;
    h.state.deleteMissing = false;
    h.state.collideOnNextCreate = false;
    h.state.execLines = undefined;
    jobCapture.jobs.length = 0;
    jobCapture.policies.length = 0;
    jobCapture.hermesEnvs.length = 0;
  });
  afterEach(async () => { await closeSandboxPools(); vi.restoreAllMocks(); });

  it("prepares capacity before the first turn and reuses the startup pool", async () => {
    const logs: string[] = [];
    const options = { agentImage: "startup-test", onLog: (line: string) => logs.push(line) };
    await prepareSandboxCapacity(options);
    expect(h.calls.filter(call => call.args.includes("create"))).toHaveLength(1);
    await makeSandboxRunCore({ ...options, modelProvider: "configured-after-startup" })(fakeInput(async () => {}));
    expect(logs.some(line => line.includes('"mode":"generic"'))).toBe(true);
    expect(logs.some(line => line.includes('"phase":"warm_miss"'))).toBe(false);
    for (const phase of ["inputs_reconcile", "workspace_upload", "inputs_sync"]) {
      expect(logs.some(line => line.includes(`"phase":"${phase}"`))).toBe(true);
    }
  });

  it("prewarms by default without an explicit pool option", async () => {
    const logs: string[] = [];
    const core = makeSandboxRunCore({ agentImage: "test", onLog: line => logs.push(line) });
    await core(fakeInput(async () => {}));
    expect(logs.some(line => line.includes('"type":"sandbox_warm"'))).toBe(true);
  });

  it("reuses only the same trusted user scope and binds providers before policy readiness", async () => {
    const logs: string[] = [];
    const core = makeSandboxRunCore({ agentImage: "test", warmPoolSize: 1,
      modelProvider: "provider", onLog: line => logs.push(line) });
    const first = fakeInput(async () => {});
    first.sandboxUser = { principalId: "alice", authorizationRevision: "v1" };
    await core(first);
    await core({ ...first, threadId: "different-chat", runId: "second" });
    expect(logs.some(line => line.includes('"mode":"user"'))).toBe(true);
    await core({ ...first, runId: "third", sandboxUser: { principalId: "alice", authorizationRevision: "v2" } });
    const commands = h.calls.map(call => call.args);
    const policies = commands.filter(args => args.includes("set"));
    expect(policies).toHaveLength(2);
    expect(commands.findIndex(args => args.includes("attach"))).toBeLessThan(commands.findIndex(args => args.includes("set")));
    expect(commands.filter(args => args.includes("create")).every(args => !args.includes("--provider"))).toBe(true);
  });

  it("streams large manifests through stdin instead of OpenShell command arguments", async () => {
    const input = fakeInput(async () => {});
    await mkdir(input.workspace.knowledgeRoot, { recursive: true });
    const files: string[] = [];
    try {
      for (let i = 0; i < 400; i++) {
        const file = join(input.workspace.knowledgeRoot, `large-manifest-${i}-${"x".repeat(80)}`);
        files.push(file); await writeFile(file, "fixture");
      }
      await makeSandboxRunCore({ agentImage: "manifest-test", onLog: () => {} })(input);
      const reconciliations = h.calls.filter(call => call.stdin !== undefined);
      expect(reconciliations.some(call => Buffer.byteLength(call.stdin!) > 32768)).toBe(true);
      for (const call of reconciliations) {
        expect(call.args.every(arg => Buffer.byteLength(arg) <= 32768)).toBe(true);
        expect(call.args).not.toContain(call.stdin);
      }
    } finally { await Promise.all(files.map(file => rm(file, { force: true }))); }
  });

  it("discards the slot without executing the agent when file reconciliation fails", async () => {
    h.state.failReconcile = true;
    const logs: string[] = [];
    const core = makeSandboxRunCore({ agentImage: "sync-failure-test", onLog: line => logs.push(line) });
    await expect(core(fakeInput(async () => {}))).rejects.toThrow(/reconciliation/);
    expect(logs.some(line => line.includes('"phase":"exec"'))).toBe(false);
    expect(h.calls.some(call => call.args.includes("delete"))).toBe(true);
  });

  it("fails closed on a rejected policy even when the CLI printed stdout", async () => {
    h.state.failPolicy = true;
    const core = makeSandboxRunCore({ agentImage: "test", warmPoolSize: 1, onLog: () => {} });
    await expect(core(fakeInput(async () => {}))).rejects.toThrow(/exited 1/);
    expect(h.calls.some(call => call.args.some(arg => arg.includes("exec node /app/entry.js")))).toBe(false);
  });

  it("tolerates cleanup when the sandbox idle timer has already deleted it", async () => {
    h.state.deleteMissing = true;
    const core = makeSandboxRunCore({ agentImage: "test", warmPoolSize: 1, onLog: () => {} });
    await expect(core(fakeInput(async () => {}))).resolves.toMatchObject({ status: "completed" });
  });

  it("omits model for hermes (it reads config.yaml, not the job)", async () => {
    const runCore = makeSandboxRunCore({ warmPoolSize: 0, agentImage: "ghcr.io/open-neko/agent:test", onLog: () => {} });
    await runCore(fakeInput(async () => {}));
    expect(jobCapture.jobs.at(-1)?.model).toBeUndefined();
  });

  it("carries configured model identity into the sandbox for attestation", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    await runCore(
      fakeInput(async () => {}, {
        id: "hermes",
        configuredIdentity: {
          provider: "gemini",
          model: "gemini-3.6-flash",
        },
        model: "gemini-3.6-flash",
        capabilities: { mcpTools: true, sessionResume: false },
        run: async () => ({ finalText: "", status: "completed" }),
      }),
    );

    expect(jobCapture.jobs.at(-1)?.configuredIdentity).toEqual({
      provider: "gemini",
      model: "gemini-3.6-flash",
    });
    expect(jobCapture.jobs.at(-1)?.model).toBeUndefined();
  });

  it("carries the per-run GraphJin policy into the OpenShell job", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    await runCore({
      ...fakeInput(async () => {}),
      graphjinToolPolicy: GRAPHJIN_DIRECT_GOVERNED_POLICY,
    });
    expect(jobCapture.jobs.at(-1)?.graphjinToolPolicy).toEqual({
      mode: "direct-governed",
    });
  });

  it("never injects direct GraphJin credentials into records turns", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    await runCore({
      ...fakeInput(async () => {}),
      dataSurface: "records",
    });
    expect(jobCapture.jobs.at(-1)).toMatchObject({
      kind: "work",
      dataSurface: "records",
    });
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinEnabled");
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinDenied");
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinServerUrl");
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinClientConfig");
  });

  it.each([
    ["false", false],
    ["true", true],
    ["null", true],
  ])("downloads only when artifacts are present or unknown (%s)", async (hint, download) => {
    h.state.execLines = [
      '__openneko_agent_result__{"status":"completed","finalText":"done"}\n',
      `__openneko_artifacts__${hint}\n`,
    ];
    const logs: string[] = [];
    await makeSandboxRunCore({ warmPoolSize: 0, agentImage: "test", onLog: (line) => logs.push(line) })(fakeInput(async () => {}));
    expect(h.calls.some((call) => call.args.includes("download"))).toBe(download);
    expect(h.calls.at(-1)?.args).toContain("delete");
    const phases = logs.filter((line) => line.startsWith("{")).map((line) => JSON.parse(line));
    expect(phases.map((p) => p.phase)).toEqual([
      "stage", "create_upload", "exec", ...(download ? ["download"] : []), "delete", "total",
    ]);
    expect(phases.every((p) => p.durationMs >= 0)).toBe(true);
  });

  it("recovers partial artifacts when the agent fails without a filesystem hint", async () => {
    h.state.execLines = [];
    await expect(makeSandboxRunCore({ warmPoolSize: 0, agentImage: "test", onLog: () => {} })(fakeInput(async () => {}))).rejects.toThrow();
    expect(h.calls.some((call) => call.args.includes("download"))).toBe(true);
    expect(h.calls.at(-1)?.args).toContain("delete");
  });

  it("applies configurable resource limits and rejects invalid quantities before spawning", async () => {
    await makeSandboxRunCore({ warmPoolSize: 0, agentImage: "test", cpu: "500m", memory: "2Gi", onLog: () => {} })(fakeInput(async () => {}));
    expect(h.calls[0]?.args).toEqual(expect.arrayContaining(["--cpu", "500m", "--memory", "2Gi"]));
    for (const cpu of ["0", "0.0", "-1", "unlimited", "1;exit"]) {
      expect(() => makeSandboxRunCore({ warmPoolSize: 0, agentImage: "test", cpu })).toThrow(/CPU limit/);
    }
    for (const memory of ["0", "-1Gi", "unlimited", "4Gi;exit"]) {
      expect(() => makeSandboxRunCore({ warmPoolSize: 0, agentImage: "test", memory })).toThrow(/memory limit/);
    }
  });

  it("creates, uploads, exec-streams, returns the result, and deletes", async () => {
    const events: AgentEvent[] = [];
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      modelHosts: [{ host: "m.example.com" }],
      keyAliases: [{ from: "api_key", to: "GEMINI_API_KEY" }],
      onLog: () => {},
    });

    const result = await runCore(fakeInput(async (e) => void events.push(e)));

    const verbs = h.calls.map((c) => c.args.find((a) => ["create", "update", "upload", "exec", "download", "delete"].includes(a)));
    // artifacts are pulled back from the box (download) before it's deleted
    expect(verbs).toEqual(["create", "exec", "download", "delete"]);
    const download = h.calls.find((c) => c.args.includes("download"));
    expect(download?.args.at(-1)).toBe(fakeInput().workspace.artifactRoot);
    // streamed event reached emit:
    expect(events.filter((event) => event.type === "message")).toEqual([
      expect.objectContaining({ type: "message", content: "hi" }),
    ]);
    expect(events.filter((event) => event.type === "status").at(-1)).toEqual({
      type: "status",
      message: "Agent is working…",
    });
    // result parsed from the RESULT line:
    expect(result).toEqual({ status: "completed", finalText: "hi there", backendState: { t: 1 } });
    // Creation does not boot Node; exec runs the standalone bundle:
    expect(h.calls[0]?.args).toContain("ghcr.io/open-neko/agent:test");
    expect(h.calls[0]?.args).toContain("--policy");
    expect(h.calls[0]?.args).toContain("--upload");
    const modelPolicy = Object.values(
      (jobCapture.policies.at(-1)?.network_policies ?? {}) as Record<
        string,
        { binaries: Array<{ path: string }>; endpoints: Array<{ host: string }> }
      >,
    ).find((policy) => policy.endpoints.some((endpoint) => endpoint.host === "m.example.com"));
    expect(modelPolicy?.binaries).toEqual([{ path: "/usr/bin/python3.11" }]);
    expect(h.calls[0]?.args.at(-1)).toBe("true");
    expect(h.calls[0]?.args).toContain("--cpu");
    expect(h.calls[0]?.args).toContain("--memory");
    expect(h.calls[0]?.args).toContain("1Gi");
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    const execCommand = execCall?.args.join(" ") ?? "";
    expect(execCommand).toContain(
      "node /app/entry.js",
    );
    // The agent self-resolves immutable image assets; the OpenShell command
    // carries no ambient image environment across the security boundary.
    expect(execCommand).not.toContain("OPENNEKO_BUILTIN_SKILLS_ROOT");
    expect(execCommand).not.toContain("OPENNEKO_MCP_BRIDGE");
    expect(execCommand).not.toContain("HERMES_DISABLE_LAZY_INSTALLS");
    // the credential alias references the OpenShell-injected var at runtime, never a value:
    expect(execCommand).toContain('export GEMINI_API_KEY="$api_key"');
  });

  it("serializes pack actions into the isolated Work job", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    const input = fakeInput(async () => {});
    input.packActions = [{
      kind: "magento.manage_catalog",
      description: "Change Magento catalog data.",
      scope: "external",
      default_mode: "ask",
    }];

    await runCore(input);

    expect(jobCapture.jobs.at(-1)).toMatchObject({
      kind: "work",
      packActions: input.packActions,
      pluginActions: [],
    });
  });

  it("serializes and drains streamed events before accepting the result", async () => {
    h.state.execLines = [
      `__openneko_event__${JSON.stringify({ type: "message", role: "assistant", content: "first" })}\n`,
      `__openneko_event__${JSON.stringify({ type: "message", role: "assistant", content: "second" })}\n`,
      `__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "firstsecond" })}\n`,
    ];
    const events: string[] = [];
    let activeEmits = 0;
    let maxActiveEmits = 0;
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    const result = await runCore(
      fakeInput(async (event) => {
        if (event.type !== "message") return;
        activeEmits += 1;
        maxActiveEmits = Math.max(maxActiveEmits, activeEmits);
        if (event.content === "first") {
          await new Promise((resolve) => setTimeout(resolve, 20));
        }
        events.push(event.content);
        activeEmits -= 1;
      }),
    );

    expect(result.status).toBe("completed");
    expect(events).toEqual(["first", "second"]);
    expect(maxActiveEmits).toBe(1);
  });

  it("rejects an empty completed result with no answer event", async () => {
    h.state.execLines = [
      `__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "", rawText: "" })}\n`,
    ];
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await expect(runCore(fakeInput(async () => {}))).rejects.toThrow(
      /completed without assistant output or surface/,
    );
  });

  it("rejects the run when ordered event delivery fails", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await expect(
      runCore(
        fakeInput(async (event) => {
          if (event.type === "message") throw new Error("database unavailable");
        }),
      ),
    ).rejects.toThrow(/agent event delivery failed: database unavailable/);
  });

  it("accepts a surface-only completed result", async () => {
    h.state.execLines = [
      `__openneko_event__${JSON.stringify({ type: "surface", messages: [{ version: "v1.0" }] })}\n`,
      `__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "", rawText: "" })}\n`,
    ];
    const events: AgentEvent[] = [];
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    const result = await runCore(
      fakeInput(async (event) => void events.push(event)),
    );

    expect(result.status).toBe("completed");
    expect(events.filter((event) => event.type === "surface")).toEqual([
      expect.objectContaining({ type: "surface" }),
    ]);
  });

  it("keeps UUID-based cold sandbox names within the OpenShell limit", async () => {
    const core = makeSandboxRunCore({agentImage:"test",warmPoolSize:0,onLog:()=>{}});
    await core({...fakeInput(async()=>{}),runId:"d77ff28f-25db-46cd-a69c-a545a9e318b5"});
    const create=h.calls.find(call=>call.args.includes("create"))!.args;
    const name=create[create.indexOf("--name")+1];
    expect(name).toMatch(/^w-[0-9a-f]{16}$/);
    expect(h.calls.filter(call=>call.args.includes("delete")).some(call=>call.args.includes(name))).toBe(true);
  });

  it.each(["collision", "lost-result"])("preserves Harness recovery evidence after %s and fences redelivery", async mode => {
    const root = await mkdtemp(join(tmpdir(), "harness-launch-test-"));
    try {
      h.state.collideOnNextCreate = mode === "collision";
      if (mode === "lost-result") h.state.execLines = [];
      const input = { ...fakeInput(async () => {}, {id:"harness",capabilities:{mcpTools:false,sessionResume:false}} as RunAgentBackendInput["backend"]), workspace:fullWorkspace(root) };
      const tokenFor=vi.fn(()=>"restricted-token");
      const core = makeSandboxRunCore({agentImage:"test",warmPoolSize:0,onLog:()=>{},brokerUrl:"http://broker",brokerTokenFor:tokenFor});
      await expect(core(input)).rejects.toThrow(mode === "collision" ? "outcome unknown" : "without a result");
      if(mode === "lost-result") expect(tokenFor).toHaveBeenCalledWith(expect.objectContaining({profile:"harness-read-only",memoryRead:true,libraryRead:true,recordsRead:true}));
      expect(h.calls.some(c=>c.args.includes("delete"))).toBe(false);
      expect(h.calls.find(c=>c.args.includes("create"))?.args).toContain("openneko.recovery=retain");
      const before=h.calls.length;
      await expect(core(input)).rejects.toThrow("outcome unknown");
      expect(h.calls.slice(before).every(c => c.args.includes("/usr/local/bin/harness-inspect"))).toBe(true);
    } finally {await rm(root,{recursive:true,force:true});}
  });

  it("grants the governed Harness broker profile only with admitted pack actions", async () => {
    const root = await mkdtemp(join(tmpdir(),"harness-profile-test-"));
    try {
      h.state.execLines = [];
      const tokenFor = vi.fn(() => "restricted-token");
      const core = makeSandboxRunCore({agentImage:"test",warmPoolSize:0,onLog:()=>{},brokerUrl:"http://broker",brokerTokenFor:tokenFor});
      const input = {...fakeInput(async()=>{}, {id:"harness",capabilities:{mcpTools:false,sessionResume:false}} as RunAgentBackendInput["backend"]),workspace:fullWorkspace(root)};
      input.packActions = [{kind:"fixture.update",description:"Update fixture",scope:"external",default_mode:"ask"}];
      await expect(core(input)).rejects.toThrow("without a result");
      expect(tokenFor).toHaveBeenCalledWith(expect.objectContaining({profile:"harness-governed",memoryRead:true,libraryRead:true,recordsRead:true}));
    } finally { await rm(root,{recursive:true,force:true}); }
  });

  it("withholds GraphJin and customer memory grants from Harness records-only turns", async () => {
    const root = await mkdtemp(join(tmpdir(), "harness-records-test-"));
    try {
      h.state.execLines = [];
      const tokenFor = vi.fn(() => "records-token");
      const core = makeSandboxRunCore({ agentImage: "test", warmPoolSize: 0, onLog: () => {}, brokerUrl: "http://broker", brokerTokenFor: tokenFor });
      const input = { ...fakeInput(async () => {}, { id: "harness", capabilities: { mcpTools: false, sessionResume: false } } as RunAgentBackendInput["backend"]), workspace: fullWorkspace(root), dataSurface: "records" as const };
      await expect(core(input)).rejects.toThrow("without a result");
      expect(tokenFor).toHaveBeenCalledWith(expect.objectContaining({ profile: "harness-read-only", recordsRead: true, lookupRead: false }));
      expect(tokenFor.mock.calls[0]?.[0]).not.toHaveProperty("memoryRead");
      expect(tokenFor.mock.calls[0]?.[0]).not.toHaveProperty("libraryRead");
    } finally { await rm(root, { recursive: true, force: true }); }
  });

  it("creates Harness with its route providers and leaves Hermes on its primary route", async () => {
    const root = await mkdtemp(join(tmpdir(), "harness-routes-test-"));
    try {
      const routing = parseHarnessRouting(JSON.stringify({context:"cheap",executor:"work",responder:"work",skill:"cheap",routes:[
        {key:"cheap",model:"fixture",url:"https://cheap.example/v1",provider:"cheap-provider",credential_env:"CHEAP_API_KEY",api_key_env:"HARNESS_CHEAP_KEY"},
        {key:"work",model:"fixture",url:"https://work.example/v1",provider:"work-provider",credential_env:"WORK_API_KEY",api_key_env:"HARNESS_WORK_KEY"},
      ]}));
      const core = makeSandboxRunCore({agentImage:"test",warmPoolSize:0,onLog:()=>{},modelProvider:"legacy-provider",
        modelHosts:[{host:"legacy.example"}],keyAliases:[{from:"api_key",to:"GEMINI_API_KEY"}],harnessRouting:routing});
      const harness = {...fakeInput(async()=>{}, {id:"harness",capabilities:{mcpTools:false,sessionResume:false}} as RunAgentBackendInput["backend"]),workspace:fullWorkspace(root)};
      h.state.execLines = [];
      await expect(core(harness)).rejects.toThrow("without a result");
      const create = h.calls.find(call=>call.args.includes("create"))!.args;
      expect(create.slice(create.indexOf("--provider"),create.indexOf("--provider")+2)).toEqual(["--provider","cheap-provider"]);
      expect(create).not.toContain("legacy-provider");
      expect(create).toContain("work-provider");
      const execIndex = h.calls.findIndex(call=>call.args.includes("exec") && !call.args.includes("/usr/local/bin/harness-inspect"));
      expect(execIndex).toBeGreaterThan(h.calls.findIndex(call=>call.args.includes("create")));
      const command = h.calls[execIndex]!.args.join(" ");
      expect(command).toContain("HARNESS_MODEL_ROUTES");
      expect(command).toContain('HARNESS_CHEAP_KEY="$CHEAP_API_KEY"');
      expect(command).toContain('HARNESS_WORK_KEY="$WORK_API_KEY"');
      expect(command).not.toContain("GEMINI_API_KEY");
    h.state.inspections = [JSON.stringify({version:1,run_id:harness.runId,outcome:"outcome_unknown",can_resume:false,operations:[]})];
    await expect(core(harness)).rejects.toThrow();
    expect(h.calls.some(call=>
      (call.args.includes("/usr/local/bin/harness-inspect") && call.args.some(arg=>arg.startsWith("HARNESS_MODEL_ROUTES="))) ||
      (call.env?.HARNESS_MODEL_ROUTES === routing.manifest && call.args.some(arg=>arg.includes("harness-inspect")))
    )).toBe(true);
      const inspection = h.calls.find(call=>call.args.includes("/usr/local/bin/harness-inspect") || call.args.some(arg=>arg.includes("harness-inspect")));
      expect(inspection?.stdin).toContain('"skill_query":"hello"');
      expect(JSON.stringify(jobCapture.policies)).toContain("cheap.example");
      expect(JSON.stringify(jobCapture.policies)).not.toContain("legacy.example");

      h.calls.length = 0;
      h.state.execLines = undefined;
      await core(fakeInput(async()=>{}));
      const hermesCreate = h.calls.find(call=>call.args.includes("create"))!.args;
      expect(hermesCreate).toEqual(expect.arrayContaining(["--provider","legacy-provider"]));
      expect(h.calls.some(call=>call.args.includes("work-provider"))).toBe(false);
      const hermesCommand = h.calls.find(call=>call.args.includes("exec"))!.args.join(" ");
      expect(hermesCommand).not.toContain("HARNESS_MODEL_ROUTES");
    } finally { await rm(root,{recursive:true,force:true}); }
  });

  it.each(["ready", "busy", "unknown", "exhausted", "broker-unknown", "stale-active", "inventory-full", "inventory-invalid", "transfer-mismatch", "absent"])("handles interrupted Harness continuation: %s", async mode => {
    const root = await mkdtemp(join(tmpdir(), "harness-continue-test-"));
    try {
      const input = { ...fakeInput(async () => {}, {id:"harness",capabilities:{mcpTools:false,sessionResume:false}} as RunAgentBackendInput["backend"]), workspace:fullWorkspace(root) };
      const core = makeSandboxRunCore({agentImage:"test",warmPoolSize:0,onLog:()=>{},env:{HARNESS_RESUME:"untrusted"}});
      h.state.execLines = [];
      await expect(core(input)).rejects.toThrow("without a result");
      h.state.execLines = undefined;
      const evidence = JSON.stringify({version:1,run_id:input.runId,outcome:mode === "unknown" ? "outcome_unknown" : "interrupted",can_resume:mode !== "exhausted",next_attempt:2,operations:[]});
      h.state.inspections = mode === "busy" ? ["busy"] : [evidence,evidence];
      if (mode === "broker-unknown") h.state.operations = [{id:1,instruction:"read",result:null}];
      if (mode === "transfer-mismatch") h.state.inspections[1] = JSON.stringify({...JSON.parse(evidence),next_attempt:3});
      if (["stale-active", "inventory-full", "inventory-invalid", "absent"].includes(mode)) {
        const state = join(input.workspace.runRoot,".harness");
        await mkdir(state,{recursive:true});
        await writeFile(join(state,createHash("sha256").update(input.runId).digest("hex")+".json"),"{}");
        if (mode === "inventory-full") h.state.inventory = JSON.stringify(Array.from({length:500},(_,i)=>({name:`other-${i}`})));
        if (mode === "inventory-invalid") h.state.inventory = "invalid";
        if (mode === "stale-active") {
          const create = h.calls.find(c=>c.args.includes("create"))!.args;
          h.state.inventory = JSON.stringify([{name:create[create.indexOf("--name")+1]}]);
          h.state.inspections = [evidence,"busy"];
        }
      }
      const before = h.calls.length;
      if (mode === "ready" || mode === "absent") {
        expect((await core(input)).status).toBe("completed");
        const calls = h.calls.slice(before);
        const download = calls.findIndex(c=>c.args.includes("download"));
        const deletion = calls.findIndex(c=>c.args.includes("delete"));
        const creation = calls.findIndex(c=>c.args.includes("create"));
        if (mode === "ready") {
          expect(download).toBeGreaterThanOrEqual(0);
          expect(deletion).toBeGreaterThan(download);
          expect(creation).toBeGreaterThan(deletion);
        } else {
          expect(creation).toBeGreaterThanOrEqual(0);
          expect(deletion).toBeGreaterThan(creation);
        }
        expect(calls.some(c=>c.args.some(arg=>arg.includes("HARNESS_RESUME='1'")))).toBe(true);
      } else {
        await expect(core(input)).rejects.toThrow();
        expect(h.calls.slice(before).some(c=>c.args.includes("create") || c.args.includes("delete"))).toBe(false);
      }
    } finally {await rm(root,{recursive:true,force:true});}
  });

  it("replaces an orphaned sandbox when a durable retry collides on the run name", async () => {
    h.state.collideOnNextCreate = true;
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore(fakeInput(async () => {}));

    const lifecycle = h.calls
      .map((call) =>
        call.args.find((arg) =>
          ["create", "exec", "download", "delete"].includes(arg),
        ),
      )
      .filter(Boolean);
    expect(lifecycle).toEqual([
      "create",
      "delete",
      "create",
      "exec",
      "download",
      "delete",
    ]);
  });

  it("aborts the live exec and deletes the whole sandbox without downloading artifacts", async () => {
    h.state.holdExec = true;
    const controller = new AbortController();
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    const pending = runCore({
      ...fakeInput(async () => {}),
      signal: controller.signal,
    });
    while (!h.calls.some((call) => call.args.includes("exec"))) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    controller.abort();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(h.calls.some((call) => call.args.includes("delete"))).toBe(true);
    expect(h.calls.some((call) => call.args.includes("download"))).toBe(false);
  });

  it("uses one OpenShell sandbox for a native-delegation-capable backend", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore(
      fakeInput(async () => {}, {
        id: "hermes",
        capabilities: {
          mcpTools: false,
          nativeDelegation: "hermes-delegate-task",
        },
      } as RunAgentBackendInput["backend"]),
    );

    const calls = h.calls.map((c) => c.args);
    expect(calls.filter((args) => args.includes("create"))).toHaveLength(1);
    expect(calls.filter((args) => args.includes("exec"))).toHaveLength(1);
    expect(calls.filter((args) => args.includes("delete"))).toHaveLength(1);
  });

  it("serializes workflow context into the sandbox job", async () => {
    const runCore = makeSandboxWorkflowRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore({ ...fakeWorkflowInput(async () => {}), timeoutMs: 1_800_000 });

    const job = jobCapture.jobs.at(-1);
    expect(job).toMatchObject({
      kind: "workflow",
      orgId: "org-1",
      threadId: "thr-1",
      runId: "run-1",
      workflowRunId: "workflow-run-1",
      mode: "headless",
      triggeredByObservationId: "obs-1",
      networkHosts: [],
      backendId: "hermes",
      message: "begin",
      agentRun: { timeoutMs: 1_800_000 },
    });
    const execArgs = h.calls.find((call) => call.args.includes("exec"))?.args ?? [];
    expect(execArgs[execArgs.indexOf("--timeout") + 1]).toBe("1920");
    expect(job).not.toHaveProperty("model");
    expect(job).not.toHaveProperty("backendState");
    expect(job).not.toHaveProperty("pluginActions");
    expect(h.calls.filter((c) => c.args.includes("create"))).toHaveLength(1);
    expect(h.calls.filter((c) => c.args.includes("exec"))).toHaveLength(1);
    expect(h.calls.filter((c) => c.args.includes("delete"))).toHaveLength(1);
  });

  it("carries script steps, input, and the run budget into the sandbox job", async () => {
    const runCore = makeSandboxWorkflowRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    const steps = [
      { id: "plan", description: "agent step" },
      {
        id: "union",
        description: "build the union",
        script: { skill: "daily-lead-union", command: ["python3", "run.py"], timeoutSeconds: 600 },
      },
    ];

    await runCore({
      ...fakeWorkflowInput(async () => {}),
      timeoutMs: 900_000,
      steps,
      input: { date: "2026-09-29" },
      reasoningEffort: "medium",
      maxToolIterations: 150,
      maxContinuations: 2,
    });

    expect(jobCapture.jobs.at(-1)).toMatchObject({
      steps,
      input: { date: "2026-09-29" },
      maxContinuations: 2,
      agentRun: { timeoutMs: 900_000, reasoningEffort: "medium", maxToolIterations: 150 },
    });
    const execArgs = h.calls.find((call) => call.args.includes("exec"))?.args ?? [];
    // 600s of steps + 3 turns of 900s + 120s margin.
    expect(execArgs[execArgs.indexOf("--timeout") + 1]).toBe("3420");
  });

  it("sizes the exec budget for steps and continuations", () => {
    const input = fakeWorkflowInput(async () => {});
    expect(workflowExecBudgetMs({ ...input, timeoutMs: 60_000 })).toBe(60_000);
    expect(
      workflowExecBudgetMs({
        ...input,
        timeoutMs: 60_000,
        maxContinuations: 1,
        steps: [{ id: "s", description: "d", script: { skill: "x", command: ["true"] } }],
      }),
    ).toBe(1_200_000 + 120_000);
  });

  it("pins an API workflow token ceiling before Harness model dispatch", async () => {
    const root = await mkdtemp(join(tmpdir(), "harness-workflow-budget-"));
    try {
      const backend = {id:"harness",capabilities:{mcpTools:false,sessionResume:false}} as RunWorkflowAgentBackendInput["backend"];
      const input = {...fakeWorkflowInput(async()=>{}, backend, fullWorkspace(root)), maxModelCalls:4, maxModelTokens:5_000, maxCostMicros:17_000,
        budgetTriageArtifactRequested: true};
      const runCore = makeSandboxWorkflowRunCore({agentImage:"test",onLog:()=>{},warmPoolSize:0});
      h.state.execLines = [];
      await expect(runCore(input)).rejects.toThrow("without a result");
      const command = h.calls.find(call=>call.args.includes("exec") && !call.args.includes("/usr/local/bin/harness-inspect"))?.args.join(" ") ?? "";
      expect(command).toContain("OPENNEKO_HARNESS_MAX_MODEL_TOKENS");
      expect(command).toContain("5000");
      expect(command).toContain("OPENNEKO_HARNESS_MAX_COST_MICROS");
      expect(command).toContain("17000");
      expect(jobCapture.jobs.at(-1)).toMatchObject({agentRun: {budgetTriageArtifactRequested: true}});
      h.state.inspections = [JSON.stringify({version:1,run_id:input.runId,outcome:"outcome_unknown",can_resume:false,operations:[]})];
      await expect(runCore(input)).rejects.toThrow();
      const inspection = h.calls.find(call=>call.args.includes("/usr/local/bin/harness-inspect") || call.args.some(arg=>arg.includes("harness-inspect")));
      expect(inspection?.stdin).toContain('"max_model_tokens":5000');
      expect(inspection?.stdin).toContain('"max_cost_micros":17000');
    } finally { await rm(root,{recursive:true,force:true}); }
  });

  it("pins a trusted workflow artifact signal in launch and recovery triage input", async () => {
    const root = await mkdtemp(join(tmpdir(), "harness-triage-artifact-"));
    const previous = process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW;
    process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW = "1";
    try {
      const price = {input_micros_per_million: 1_000_000, output_micros_per_million: 1_000_000};
      const routing = parseHarnessRouting(JSON.stringify({context: "work", executor: "work", responder: "work",
        triage: "triage", pricing_version: "artifact-test-v1", routes: [
          {key: "work", model: "fixture", url: "https://work.example/v1", provider: "work-provider",
            credential_env: "WORK_API_KEY", api_key_env: "HARNESS_WORK_KEY", price},
          {key: "triage", model: "jev-fixture", url: "https://triage.example", provider: "triage-provider",
            credential_env: "TRIAGE_API_KEY", api_key_env: "HARNESS_TRIAGE_KEY", price},
        ]}));
      const backend = {id: "harness", capabilities: {mcpTools: false, sessionResume: false}} as RunWorkflowAgentBackendInput["backend"];
      const input = {...fakeWorkflowInput(async () => {}, backend, fullWorkspace(root)), maxCostMicros: 17_000,
        budgetTriageArtifactRequested: true};
      const core = makeSandboxWorkflowRunCore({agentImage: "test", onLog: () => {}, warmPoolSize: 0,
        harnessRouting: routing});
      h.state.execLines = [];
      await expect(core(input)).rejects.toThrow("without a result");
      expect(jobCapture.jobs.at(-1)).toMatchObject({agentRun: {budgetTriageArtifactRequested: true}});
      h.state.inspections = [JSON.stringify({version: 1, run_id: input.runId, outcome: "outcome_unknown",
        can_resume: false, operations: []})];
      await expect(core(input)).rejects.toThrow();
      const inspect = h.calls.find(call => call.args.some(arg => arg.includes("harness-inspect")) && call.stdin?.includes('"triage_artifact_requested":true'));
      expect(inspect?.stdin).toContain('"triage_tool_families":"file,graphjin,workflow"');
    } finally {
      if (previous === undefined) delete process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW;
      else process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW = previous;
      await rm(root, {recursive: true, force: true});
    }
  });

  it("adds pack-declared workflow hosts to the OpenShell policy", async () => {
    const runCore = makeSandboxWorkflowRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    const input = fakeWorkflowInput(async () => {});
    input.networkHosts = ["helpx.adobe.com", "experienceleague.adobe.com"];

    await runCore(input);

    const policies = Object.values(
      (jobCapture.policies.at(-1)?.network_policies ?? {}) as Record<
        string,
        { binaries: Array<{ path: string }>; endpoints: Array<{ host: string }> }
      >,
    );
    const workflowPolicy = policies.find(
      (policy) => policy.binaries[0]?.path === "/usr/bin/python3.11",
    );
    expect(workflowPolicy?.endpoints.map((endpoint) => endpoint.host)).toEqual([
      "experienceleague.adobe.com",
      "helpx.adobe.com",
    ]);
  });

  it("does not serialize GraphJin credentials into workflow sandbox jobs", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "ws-"));
    const workspace = fullWorkspace(orgRoot);
    const clientDir = join(workspace.runRoot, "gj-auth", "graphjin");
    await mkdir(clientDir, { recursive: true });
    await writeFile(
      join(clientDir, "client.json"),
      JSON.stringify({
        server: "http://localhost:8080/api/v1/mcp",
        token: "run-token",
        subject: "service",
      }),
    );
    const runCore = makeSandboxWorkflowRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore(fakeWorkflowInput(async () => {}, undefined, workspace));

    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinClientConfig");
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinServerUrl");
  });

  it("gives model-only jobs no GraphJin capability or artifact persistence", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "job-ws-"));
    const workspace = fullWorkspace(orgRoot);
    await mkdir(workspace.artifactRoot, { recursive: true });
    const runCore = makeSandboxJobRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore(fakeJobInput(workspace));

    expect(jobCapture.jobs.at(-1)).toMatchObject({
      kind: "agent-job",
      agentAccess: {},
      agentRun: {
        timeoutMs: 45_000,
        retries: 0,
        tag: "profile-job",
      },
    });
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinServerUrl");
    expect(jobCapture.jobs.at(-1)).not.toHaveProperty("graphjinClientConfig");
    expect(h.calls.some((c) => c.args.includes("download"))).toBe(false);
  });

  it("gives GraphJin jobs a broker capability without source egress or tokens", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "job-ws-"));
    const workspace = fullWorkspace(orgRoot);
    const clientDir = join(workspace.runRoot, "gj-auth", "graphjin");
    await mkdir(clientDir, { recursive: true });
    await writeFile(
      join(clientDir, "client.json"),
      JSON.stringify({
        server: "http://localhost:8080/api/v1/mcp",
        token: "ephemeral-job-token",
        subject: "service",
      }),
    );
    const runCore = makeSandboxJobRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await runCore(fakeJobInput(workspace, { graphjinRead: true }));

    const job = jobCapture.jobs.at(-1);
    expect(job).toMatchObject({
      kind: "agent-job",
      agentAccess: { graphjinRead: true },
    });
    expect(job).not.toHaveProperty("graphjinWriteGrants");
    expect(job).not.toHaveProperty("graphjinServerUrl");
    expect(job).not.toHaveProperty("graphjinClientConfig");

    await runCore(fakeJobInput(workspace, { graphjinAgent: true }));
    expect(jobCapture.jobs.at(-1)).toMatchObject({
      kind: "agent-job",
      agentAccess: { graphjinAgent: true },
    });
  });

  it("scopes broker egress to node, injects url+token, and releases on finish", async () => {
    const released: string[] = [];
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      brokerUrl: "http://host.openshell.internal:4199",
      brokerTokenFor: ({ runId, orgId }) => `tok-${orgId}-${runId}`,
      brokerRelease: (runId) => released.push(runId),
      onLog: () => {},
    });

    await runCore(fakeInput(async () => {}));

    // the creation policy opens the broker host:port for the node binary only:
    const policies = Object.values(
      (jobCapture.policies.at(-1)?.network_policies ?? {}) as Record<
        string,
        { binaries: Array<{ path: string }>; endpoints: Array<{ host: string; port: number }> }
      >,
    );
    const nodePolicy = policies.find(
      (policy) => policy.binaries[0]?.path === "/usr/local/bin/node",
    );
    expect(nodePolicy?.endpoints).toContainEqual(
      expect.objectContaining({ host: "host.openshell.internal", port: 4199 }),
    );
    // the box gets the broker url + a run-bound bearer token — never a raw secret:
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    expect(execCall?.args.join(" ")).toContain(
      "OPENNEKO_BROKER_URL='http://host.openshell.internal:4199'",
    );
    expect(execCall?.args.join(" ")).toContain("OPENNEKO_BROKER_TOKEN='tok-org-1-run-1'");
    // the token is dropped when the run ends:
    expect(released).toEqual(["run-1"]);
  });

  it("omits broker env when no broker is wired (hermes-only path)", async () => {
    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    await runCore(fakeInput(async () => {}));
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    expect(execCall?.args.join(" ")).not.toContain("OPENNEKO_BROKER_URL");
    expect(execCall?.args.join(" ")).not.toContain("OPENNEKO_BROKER_TOKEN");
  });

  it("mirrors HERMES_HOME keyless and points the box at it", async () => {
    const hostHome = await mkdtemp(join(tmpdir(), "hh-"));
    await writeFile(join(hostHome, "config.yaml"), 'model:\n  provider: "gemini"\n');
    await writeFile(join(hostHome, ".env"), "GEMINI_API_KEY=REAL_SECRET\n");

    const runCore = makeSandboxRunCore({
      warmPoolSize: 0,
      agentImage: "ghcr.io/open-neko/agent:test",
      hermesHomeHostPath: hostHome,
      keyAliases: [{ from: "api_key", to: "GEMINI_API_KEY" }],
      onLog: () => {},
    });
    await runCore(fakeInput(async () => {}));

    // The minimal workspace, job descriptor, and keyless Hermes config cross
    // the boundary together in the create transaction.
    expect(h.calls.filter((c) => c.args.includes("upload"))).toHaveLength(0);
    const create = h.calls.find((c) => c.args.includes("create"));
    expect(create?.args).toContain("--upload");
    // the box reads the mirror, not a host path:
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    expect(execCall?.args.join(" ")).toContain(
      "HERMES_HOME='/sandbox/org-1/runs/run-1/.openneko/hermes-home'",
    );
    expect(jobCapture.hermesEnvs).toEqual([""]);
    expect(
      JSON.stringify({
        calls: h.calls,
        jobs: jobCapture.jobs,
        policies: jobCapture.policies,
      }),
    ).not.toContain("REAL_SECRET");
  });
});

describe("ensureOpenShellProvider", () => {
  beforeEach(() => {
    h.calls.length = 0;
  });
  afterEach(() => vi.restoreAllMocks());

  it("registers the generic profile and creates the provider with the key", async () => {
    await ensureOpenShellProvider({ providerName: "org-x", apiKey: "SECRET-KEY" });
    const lines = h.calls.map((c) => c.args.join(" "));
    // generic profile imported (idempotent):
    expect(lines.some((l) => l.startsWith("provider profile import --file") && l.endsWith(".yaml"))).toBe(true);
    // provider created from the generic type, holding the key:
    const create = lines.find((l) => l.startsWith("provider create"));
    expect(create).toContain("--name org-x");
    expect(create).toContain("--type openneko-agent");
    expect(create).toContain("--credential api_key=SECRET-KEY");
  });
});

describe("deleteOpenShellProvider", () => {
  beforeEach(() => {
    h.calls.length = 0;
  });

  it("deletes only the explicitly named provider", async () => {
    await deleteOpenShellProvider({
      providerName: "openneko-agent-eval-org",
      gatewayName: "openneko",
    });
    expect(h.calls).toEqual([
      {
        args: [
          "--gateway",
          "openneko",
          "provider",
          "delete",
          "openneko-agent-eval-org",
        ],
      },
    ]);
  });
});

describe("verifyOpenShellGateway", () => {
  beforeEach(() => {
    h.calls.length = 0;
  });

  it("uses a read-only gateway RPC with the configured registration", async () => {
    await verifyOpenShellGateway({ gatewayName: "openneko" });
    expect(h.calls).toEqual([
      { args: ["--gateway", "openneko", "provider", "list", "--names"] },
    ]);
  });
});
