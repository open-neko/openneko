/**
 * HermesBackend spawn invariants — narrow tests around the env / argv
 * passed to `hermes` so a regression on the global-config rule doesn't
 * silently re-introduce the per-org HERMES_HOME bug we fixed.
 *
 * We mock node:child_process.spawn to capture what HermesBackend would
 * have invoked and then immediately resolve the run, so no real binary
 * is exec'd.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type SpawnCall = {
  command: string;
  args: string[];
  options: { cwd?: string; env?: NodeJS.ProcessEnv };
};

const spawnCalls: SpawnCall[] = [];

vi.mock("node:child_process", async () => {
  const actual = await vi.importActual<typeof import("node:child_process")>(
    "node:child_process",
  );
  return {
    ...actual,
    spawn: (
      command: string,
      args: string[],
      options: { cwd?: string; env?: NodeJS.ProcessEnv },
    ) => {
      spawnCalls.push({ command, args, options });
      const stdout = makeStream("OK");
      const stderr = makeStream("");
      const child = {
        pid: 12345,
        stdout,
        stderr,
        on(event: string, cb: (...a: unknown[]) => void) {
          if (event === "close") {
            // Defer so the listeners on stdout/stderr land first.
            setImmediate(() => cb(0));
          }
          return child;
        },
        kill() {},
      };
      return child as unknown as ReturnType<typeof actual.spawn>;
    },
  };
});

function makeStream(text: string) {
  const listeners = new Map<string, Array<(arg: unknown) => void>>();
  const stream = {
    on(event: string, cb: (arg: unknown) => void) {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(cb);
      if (event === "data" && text) {
        setImmediate(() => cb(Buffer.from(text)));
      }
      return stream;
    },
  };
  return stream;
}

beforeEach(() => {
  spawnCalls.length = 0;
});

afterEach(() => {
  vi.clearAllMocks();
});

const { HermesBackend } = await import("../src/agent-backends/hermes");

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
  it("never sets HERMES_HOME — always inherits the global ~/.hermes config", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "ping", workspace: FAKE_WORKSPACE });

    expect(spawnCalls).toHaveLength(1);
    const env = spawnCalls[0].options.env ?? {};
    // Must not be set by the backend; if the user has it in their shell
    // env we inherit (...process.env) but never override it.
    if (process.env.HERMES_HOME) {
      expect(env.HERMES_HOME).toBe(process.env.HERMES_HOME);
    } else {
      expect(env.HERMES_HOME).toBeUndefined();
    }
  });

  it("invokes hermes with -z prompt as args", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "explain Q1 revenue" });
    expect(spawnCalls[0].command).toBe("hermes");
    expect(spawnCalls[0].args).toEqual(["-z", "explain Q1 revenue"]);
  });

  it("appends userMessage to prompt when supplied", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "system instructions", userMessage: "hi" });
    const argText = spawnCalls[0].args[1];
    expect(argText).toContain("system instructions");
    expect(argText).toContain("hi");
  });

  it("forwards --skills <list> when skills are passed", async () => {
    const backend = new HermesBackend();
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      skills: ["pdf", "xlsx"],
    });
    expect(spawnCalls[0].args).toContain("--skills");
    const skillsIdx = spawnCalls[0].args.indexOf("--skills");
    expect(spawnCalls[0].args[skillsIdx + 1]).toBe("pdf,xlsx");
  });

  it("uses workspace.orgRoot as cwd in streaming mode", async () => {
    const backend = new HermesBackend();
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
    });
    expect(spawnCalls[0].options.cwd).toBe(FAKE_WORKSPACE.orgRoot);
  });

  it("prepends workspace.binRoot to PATH (so the per-run graphjin guard wins)", async () => {
    const backend = new HermesBackend();
    await backend.run({ prompt: "p", workspace: FAKE_WORKSPACE });
    const path = spawnCalls[0].options.env?.PATH ?? "";
    expect(path.startsWith(`${FAKE_WORKSPACE.binRoot}:`)).toBe(true);
  });

  it("emits status + message events when onEvent is provided", async () => {
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
