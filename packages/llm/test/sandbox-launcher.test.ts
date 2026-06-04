import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent } from "../src/agent-backend";
import type { RunAgentBackendInput } from "../src/work/agent-core";

/**
 * The launcher shells out to the `openshell` CLI. We mock spawn: non-exec calls
 * (create/upload/delete) return empty stdout + exit 0; the `exec` call returns
 * a stdout stream carrying tagged EVENT + RESULT lines, which the launcher must
 * relay to `emit` and parse into the AgentRunResult.
 */
const h = vi.hoisted(() => {
  const calls: { args: string[] }[] = [];
  function spawn(_cmd: string, args: string[]) {
    calls.push({ args });
    const isExec = args.includes("exec");
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
    const stderr = Readable.from([]);
    const lines = isExec
      ? [
          'noise before\n',
          `\n__openneko_event__${JSON.stringify({ type: "message", role: "assistant", content: "hi" })}\n`,
          `\n__openneko_agent_result__${JSON.stringify({ status: "completed", finalText: "hi there", backendState: { t: 1 } })}\n`,
        ]
      : [];
    let closed = false;
    const closeOnce = () => {
      if (closed) return;
      closed = true;
      fire(ch, "close", 0);
    };
    const stdout = Readable.from(lines);
    stdout.on("end", () => queueMicrotask(closeOnce));
    return { stdout, stderr, on: reg(ch), kill() {} };
  }
  return { calls, spawn };
});

vi.mock("node:child_process", () => ({ spawn: h.spawn }));

// Capture the job descriptor the launcher writes (then uploads to the box), so
// we can assert what crosses the host→sandbox boundary — e.g. the Claude model,
// which the box needs to reconstruct the claude-agent backend.
const jobCapture = vi.hoisted(() => ({ jobs: [] as Array<Record<string, unknown>> }));
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
      return (actual.writeFile as (...a: unknown[]) => Promise<void>)(p, data, ...rest);
    },
  };
});

const { makeSandboxRunCore, buildModelEgressArgs, ensureOpenShellProvider } =
  await import("../src/work/sandbox-launcher");

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
    workspace: { orgRoot: "/tmp/ws/org-1" } as RunAgentBackendInput["workspace"],
    backendState: undefined,
    pluginActions: [],
    emit,
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
});

describe("makeSandboxRunCore", () => {
  beforeEach(() => {
    h.calls.length = 0;
    jobCapture.jobs.length = 0;
  });
  afterEach(() => vi.restoreAllMocks());

  it("threads the claude model into the box job (box rebuilds the backend with it)", async () => {
    const runCore = makeSandboxRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      onLog: () => {},
    });
    await runCore(
      fakeInput(async () => {}, {
        id: "claude-agent",
        model: "claude-sonnet-4-6",
        capabilities: { mcpTools: true },
      } as RunAgentBackendInput["backend"]),
    );
    const job = jobCapture.jobs.at(-1);
    expect(job?.backendId).toBe("claude-agent");
    // Without this, the box builds claude-agent with model:undefined and throws
    // "requires a Claude model" — the whole claude sandbox path is dead.
    expect(job?.model).toBe("claude-sonnet-4-6");
  });

  it("omits model for hermes (it reads config.yaml, not the job)", async () => {
    const runCore = makeSandboxRunCore({ agentImage: "ghcr.io/open-neko/agent:test", onLog: () => {} });
    await runCore(fakeInput(async () => {}));
    expect(jobCapture.jobs.at(-1)?.model).toBeUndefined();
  });

  it("creates, uploads, exec-streams, returns the result, and deletes", async () => {
    const events: AgentEvent[] = [];
    const runCore = makeSandboxRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      modelEgress: [{ host: "m.example.com", binary: "node" }],
      keyAliases: [{ from: "api_key", to: "GEMINI_API_KEY" }],
      onLog: () => {},
    });

    const result = await runCore(fakeInput(async (e) => void events.push(e)));

    const verbs = h.calls.map((c) => c.args.find((a) => ["create", "update", "upload", "exec", "delete"].includes(a)));
    expect(verbs).toEqual(["create", "update", "upload", "upload", "exec", "delete"]);
    // streamed event reached emit:
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ type: "message", content: "hi" });
    // result parsed from the RESULT line:
    expect(result).toEqual({ status: "completed", finalText: "hi there", backendState: { t: 1 } });
    // create used the agent image; exec ran entry.ts via the sh-wrapper:
    expect(h.calls[0]?.args).toContain("ghcr.io/open-neko/agent:test");
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    expect(execCall?.args.join(" ")).toContain("agent-sandbox/entry.ts");
    // the credential alias references the OpenShell-injected var at runtime, never a value:
    expect(execCall?.args.join(" ")).toContain('export GEMINI_API_KEY="$api_key"');
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

    // the policy update opens the broker host:port for the node binary only:
    const update = h.calls.find((c) => c.args.includes("update"));
    expect(update?.args.join(" ")).toContain(
      "host.openshell.internal:4199:read-write:rest:enforce",
    );
    expect(update?.args).toContain("/usr/local/bin/node");
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

    // three uploads now: workspace, keyless hermes-home, job descriptor
    const uploads = h.calls.filter((c) => c.args.includes("upload"));
    expect(uploads).toHaveLength(3);
    const hermesUpload = uploads.find((u) => u.args.some((a) => a.endsWith("/hermes-home")));
    expect(hermesUpload?.args).toContain("/sandbox");
    // the box reads the mirror, not a host path:
    const execCall = h.calls.find((c) => c.args.includes("exec"));
    expect(execCall?.args.join(" ")).toContain("HERMES_HOME='/sandbox/hermes-home'");
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
