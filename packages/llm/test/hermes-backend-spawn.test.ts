import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMockSpawn, makeController } from "./helpers/fake-acp-process";

const controller = makeController();

vi.mock("node:child_process", async () => {
  const actual = await vi.importActual<typeof import("node:child_process")>(
    "node:child_process",
  );
  return {
    ...actual,
    spawn: createMockSpawn(controller),
  };
});

beforeEach(() => {
  controller.spawnCalls.length = 0;
  controller.setScript({});
});

afterEach(() => {
  vi.clearAllMocks();
});

const { HermesBackend } = await import("../src/agent-backends/hermes");
const { hermesHomeForOrg } = await import("../src/host-provision");

const FAKE_WORKSPACE = {
  orgRoot: "/tmp/neko-test/org",
  skillsRoot: "/tmp/neko-test/org/skills",
  memoryRoot: "/tmp/neko-test/org/memory",
  knowledgeRoot: "/tmp/neko-test/org/knowledge",
  uploadsRoot: "/tmp/neko-test/org/uploads",
  runsRoot: "/tmp/neko-test/org/runs",
  threadUploadsRoot: "/tmp/neko-test/org/uploads/t1",
  runRoot: "/tmp/neko-test/org/runs/r1",
  artifactRoot: "/tmp/neko-test/org/runs/r1/artifacts",
  binRoot: "/tmp/neko-test/org/runs/r1/bin",
  claudeProjectRoot: "/tmp/neko-test/org",
  claudeConfigRoot: "/tmp/neko-test/org/claude/config",
} as const;

describe("HermesBackend spawn invariants", () => {
  it("sets HERMES_HOME to the per-org path when orgId is supplied", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "ping", orgId: "org-abc-123", workspace: FAKE_WORKSPACE });

    expect(controller.spawnCalls).toHaveLength(1);
    const env = controller.spawnCalls[0].options.env ?? {};
    expect(env.HERMES_HOME).toBe(hermesHomeForOrg("org-abc-123"));
  });

  it("leaves HERMES_HOME alone when orgId is absent (debug / legacy callers)", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "ping", workspace: FAKE_WORKSPACE });

    expect(controller.spawnCalls).toHaveLength(1);
    const env = controller.spawnCalls[0].options.env ?? {};
    if (process.env.HERMES_HOME) {
      expect(env.HERMES_HOME).toBe(process.env.HERMES_HOME);
    } else {
      expect(env.HERMES_HOME).toBeUndefined();
    }
  });

  it("invokes hermes with `acp --accept-hooks` (no positional prompt)", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "explain Q1 revenue" });
    expect(controller.spawnCalls[0].command).toBe("hermes");
    expect(controller.spawnCalls[0].args).toEqual(["acp", "--accept-hooks"]);
  });

  it("does not leak prompt text into argv (prompt goes over JSON-RPC stdin)", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "system instructions", userMessage: "hi" });
    const args = controller.spawnCalls[0].args;
    expect(args).not.toContain("system instructions");
    expect(args).not.toContain("hi");
  });

  it("uses workspace.orgRoot as cwd in streaming mode", async () => {
    const backend = new HermesBackend();
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
    });
    expect(controller.spawnCalls[0].options.cwd).toBe(FAKE_WORKSPACE.orgRoot);
  });

  it("prepends workspace.binRoot to PATH (so the per-run graphjin guard wins)", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "p", workspace: FAKE_WORKSPACE });
    const path = controller.spawnCalls[0].options.env?.PATH ?? "";
    expect(path.startsWith(`${FAKE_WORKSPACE.binRoot}:`)).toBe(true);
  });

  it("spawns with detached:true so we own the process group for SIGKILL", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "p", workspace: FAKE_WORKSPACE });
    expect(controller.spawnCalls[0].options.detached).toBe(true);
  });

  it("emits status + message events when onEvent is provided", async () => {
    controller.setScript({
      notificationsByMethod: {
        "session/prompt": [
          {
            jsonrpc: "2.0",
            method: "session/update",
            params: {
              sessionId: "sess-1",
              update: {
                sessionUpdate: "agent_message_chunk",
                content: { type: "text", text: "OK" },
              },
            },
          },
        ],
      },
    });
    const backend = new HermesBackend();
    const events: Array<{ type: string }> = [];
    const result = await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (event) => {
        events.push(event);
      },
    });
    expect(result.status).toBe("completed");
    expect(events.find((e) => e.type === "status")).toBeDefined();
    expect(events.find((e) => e.type === "message")).toBeDefined();
  });

  it("returns AgentRunResult (no thrown error) on completed run", async () => {
    const backend = new HermesBackend();
    const result = await backend.run({ prompt: "p" });
    expect(result.status).toBe("completed");
    expect(typeof result.finalText).toBe("string");
  });
});
