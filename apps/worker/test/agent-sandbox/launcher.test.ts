import { Readable } from "node:stream";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentEvent } from "@neko/llm";
import type { RunAgentBackendInput } from "@neko/llm/work";

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

const { makeSandboxRunCore, buildModelEgressArgs } = await import(
  "../../src/agent-sandbox/launcher"
);

function fakeInput(emit: (e: AgentEvent) => Promise<void>): RunAgentBackendInput {
  return {
    backend: { id: "hermes", capabilities: { mcpTools: false } } as RunAgentBackendInput["backend"],
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
});

describe("makeSandboxRunCore", () => {
  beforeEach(() => {
    h.calls.length = 0;
  });
  afterEach(() => vi.restoreAllMocks());

  it("creates, uploads, exec-streams, returns the result, and deletes", async () => {
    const events: AgentEvent[] = [];
    const runCore = makeSandboxRunCore({
      agentImage: "ghcr.io/open-neko/agent:test",
      modelEgress: [{ host: "m.example.com", binary: "node" }],
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
  });
});
