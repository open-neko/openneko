import {
  access,
  cp,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import type { RunAgentBackendInput } from "../src/work/agent-core";
import { GRAPHJIN_DIRECT_GOVERNED_POLICY } from "../src/work/graphjin-tool-policy";
import type { RunWorkflowAgentBackendInput } from "../src/workflows/agent-core";

/**
 * The launcher shells out to the `openshell` CLI. We mock spawn: non-exec calls
 * (create/upload/delete) return empty stdout + exit 0; the `exec` call returns
 * a stdout stream carrying tagged EVENT + RESULT lines, which the launcher must
 * relay to `emit` and parse into the AgentRunResult.
 */
const h = vi.hoisted(() => {
  const calls: { args: string[] }[] = [];
  const state = {
    holdExec: false,
    collideOnNextCreate: false,
    execLines: undefined as string[] | undefined,
  };
  function spawn(_cmd: string, args: string[]) {
    calls.push({ args });
    const isExec = args.includes("exec");
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
      createCollision
        ? ["Error: × sandbox 'work-run-1' already exists\n"]
        : [],
    );
    const lines = isExec
      ? state.execLines ?? [
          'noise before\n',
          `\n__openneko_event__${JSON.stringify({ type: "message", role: "assistant", content: "hi" })}\n`,
          `\n__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "hi there", backendState: { t: 1 } })}\n`,
        ]
      : [];
    let closed = false;
    const closeOnce = () => {
      if (closed) return;
      closed = true;
      fire(ch, "close", createCollision ? 1 : 0);
    };
    const stdout = Readable.from(lines);
    if (!isExec || !state.holdExec) {
      stdout.on("end", () => queueMicrotask(closeOnce));
    }
    return {
      stdout,
      stderr,
      on: reg(ch),
      kill() {
        closeOnce();
        return true;
      },
    };
  }
  return { calls, state, spawn };
});

vi.mock("node:child_process", () => ({ spawn: h.spawn }));

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
  makeSandboxJobRunCore,
  makeSandboxWorkflowRunCore,
  buildModelEgressArgs,
  buildSandboxPolicy,
  buildScopedEgressArgs,
  deleteOpenShellProvider,
  ensureOpenShellProvider,
  sandboxLauncherOptionsFromConfig,
  sandboxLauncherOptionsFromEnv,
  stageSandboxWorkspace,
  verifyOpenShellGateway,
} = await import("../src/work/sandbox-launcher");

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
    h.state.holdExec = false;
    h.state.collideOnNextCreate = false;
    h.state.execLines = undefined;
    jobCapture.jobs.length = 0;
    jobCapture.policies.length = 0;
    jobCapture.hermesEnvs.length = 0;
  });
  afterEach(() => vi.restoreAllMocks());

  it("omits model for hermes (it reads config.yaml, not the job)", async () => {
    const runCore = makeSandboxRunCore({ agentImage: "ghcr.io/open-neko/agent:test", onLog: () => {} });
    await runCore(fakeInput(async () => {}));
    expect(jobCapture.jobs.at(-1)?.model).toBeUndefined();
  });

  it("carries configured model identity into the sandbox for attestation", async () => {
    const runCore = makeSandboxRunCore({
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

  it("creates, uploads, exec-streams, returns the result, and deletes", async () => {
    const events: AgentEvent[] = [];
    const runCore = makeSandboxRunCore({
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
    // create used the agent image; preflight and exec ran the deployed
    // workspace entry through its production tsx dependency:
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
    expect(h.calls[0]?.args.join(" ")).toContain(
      "node --import tsx/esm /app/src/agent-sandbox/entry.ts --preflight",
    );
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    const execCommand = execCall?.args.join(" ") ?? "";
    expect(execCommand).toContain(
      "node --import tsx/esm /app/src/agent-sandbox/entry.ts",
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
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });

    await expect(runCore(fakeInput(async () => {}))).rejects.toThrow(
      /completed without assistant output or surface/,
    );
  });

  it("rejects the run when ordered event delivery fails", async () => {
    const runCore = makeSandboxRunCore({
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

  it("replaces an orphaned sandbox when a durable retry collides on the run name", async () => {
    h.state.collideOnNextCreate = true;
    const runCore = makeSandboxRunCore({
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

    await runCore(fakeWorkflowInput(async () => {}));

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
    });
    expect(job).not.toHaveProperty("model");
    expect(job).not.toHaveProperty("backendState");
    expect(job).not.toHaveProperty("pluginActions");
    expect(h.calls.filter((c) => c.args.includes("create"))).toHaveLength(1);
    expect(h.calls.filter((c) => c.args.includes("exec"))).toHaveLength(1);
    expect(h.calls.filter((c) => c.args.includes("delete"))).toHaveLength(1);
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
