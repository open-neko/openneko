import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  assistantText,
  assistantToolUse,
  createMockClaudeQuery,
  makeClaudeMockController,
  resultError,
  resultSuccess,
  systemInit,
  userToolResult,
} from "./helpers/fake-claude-sdk";

const claudeController = makeClaudeMockController();

vi.mock("node:child_process", async () => {
  const actual = await vi.importActual<typeof import("node:child_process")>(
    "node:child_process",
  );
  return {
    ...actual,
    spawnSync: () => ({ status: 0, stdout: Buffer.from(""), stderr: Buffer.from("") }),
  };
});

vi.mock("@anthropic-ai/claude-agent-sdk", async () => {
  const actual = await vi.importActual<typeof import("@anthropic-ai/claude-agent-sdk")>(
    "@anthropic-ai/claude-agent-sdk",
  );
  return {
    ...actual,
    query: createMockClaudeQuery(claudeController),
  };
});

beforeEach(() => {
  claudeController.calls.length = 0;
  claudeController.setScript({});
});

afterEach(() => {
  vi.clearAllMocks();
});

const { ClaudeAgentBackend, mergeHooks } = await import(
  "../src/agent-backends/claude-agent"
);

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

const CONFIG = { apiKey: "sk-ant-test", model: "claude-sonnet-4-6" };

describe("ClaudeAgentBackend run", () => {
  it("forwards opts.skills when provided, defaults to 'all' otherwise", async () => {
    claudeController.setScript({
      records: [systemInit("sess-1"), resultSuccess("sess-1", "ok")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
    });
    expect(claudeController.lastOptions()?.skills).toBe("all");

    claudeController.calls.length = 0;
    claudeController.setScript({
      records: [systemInit("sess-2"), resultSuccess("sess-2", "ok")],
    });
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      skills: ["pdf", "docx"],
    });
    expect(claudeController.lastOptions()?.skills).toEqual(["pdf", "docx"]);
  });

  it("resumes from backendState['claude-agent'].sessionId and persists fresh sessionId", async () => {
    claudeController.setScript({
      records: [systemInit("new-sess"), resultSuccess("new-sess", "hi")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      backendState: { "claude-agent": { sessionId: "prior-sess" } },
    });
    expect(claudeController.lastOptions()?.resume).toBe("prior-sess");
    expect(result.backendState).toEqual({ "claude-agent": { sessionId: "new-sess" } });
  });

  it("emits tool_start then tool_end with matching id, plus duration_ms via PostToolUse hook", async () => {
    claudeController.setScript({
      records: [
        systemInit("sess-tool"),
        assistantToolUse("tu-1", "Read", { path: "/x.txt" }),
        userToolResult("tu-1", "file contents"),
        resultSuccess("sess-tool", "Done"),
      ],
    });
    const events: Array<Record<string, unknown>> = [];
    const backend = new ClaudeAgentBackend(CONFIG);
    const runPromise = backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (e) => {
        events.push(e as Record<string, unknown>);
      },
    });

    await new Promise<void>((resolve) => {
      const tick = () => (claudeController.lastOptions() ? resolve() : setTimeout(tick, 5));
      tick();
    });
    const opts = claudeController.lastOptions()!;
    const hooks = opts.hooks as Record<string, Array<{ hooks: Array<(input: Record<string, unknown>) => Promise<unknown>> }>>;
    const postToolUse = hooks.PostToolUse[0].hooks[0];
    await postToolUse({ tool_use_id: "tu-1", duration_ms: 42 });

    await runPromise;

    const toolStart = events.find((e) => e.type === "tool_start");
    const toolEnd = events.find((e) => e.type === "tool_end");
    expect(toolStart?.id).toBe("tu-1");
    expect(toolStart?.name).toBe("Read");
    expect(toolEnd?.id).toBe("tu-1");
    expect(toolEnd?.result).toBe("file contents");
    const durationDelta = events.find(
      (e) => e.type === "tool_delta" && (e.delta as { durationMs?: number }).durationMs === 42,
    );
    expect(durationDelta).toBeDefined();
  });

  it("emits tool_end with error when tool_result has is_error=true", async () => {
    claudeController.setScript({
      records: [
        systemInit("sess-err"),
        assistantToolUse("tu-2", "Bash", { cmd: "false" }),
        userToolResult("tu-2", "command failed", true),
        resultSuccess("sess-err", "noted"),
      ],
    });
    const events: Array<Record<string, unknown>> = [];
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (e) => {
        events.push(e as Record<string, unknown>);
      },
    });
    const toolEnd = events.find((e) => e.type === "tool_end");
    expect(toolEnd?.error).toBe("command failed");
  });

  it("dedupes streamed assistant message events by accumulated content", async () => {
    claudeController.setScript({
      records: [
        systemInit("sess-msg"),
        assistantText("hello"),
        assistantText("hello"),
        assistantText("hello world"),
        resultSuccess("sess-msg", "hello world"),
      ],
    });
    const events: Array<Record<string, unknown>> = [];
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (e) => {
        events.push(e as Record<string, unknown>);
      },
    });
    const messages = events.filter((e) => e.type === "message").map((e) => e.content);
    expect(messages).toEqual(["hello", "hello world", "hello world"]);
    expect(result.finalText).toBe("hello world");
  });

  it("emits surface event AND strips fence from finalText when onEvent is provided", async () => {
    const fenced =
      "Here:\n```neko_a2ui\n[{\"version\":\"v0.9\",\"kind\":\"text\",\"body\":\"hi\"}]\n```\nDone.";
    claudeController.setScript({
      records: [systemInit("sess-surf"), resultSuccess("sess-surf", fenced)],
    });
    const events: Array<Record<string, unknown>> = [];
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (e) => {
        events.push(e as Record<string, unknown>);
      },
    });
    expect(events.find((e) => e.type === "surface")).toBeDefined();
    expect(result.finalText).not.toContain("neko_a2ui");
    expect(result.finalText).toContain("Done.");
  });

  it("leaves fence in finalText when onEvent is absent (sync mode)", async () => {
    const fenced = "```neko_a2ui\n[]\n```";
    claudeController.setScript({
      records: [systemInit("sess-sync"), resultSuccess("sess-sync", fenced)],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({ prompt: "p" });
    expect(result.finalText).toContain("neko_a2ui");
  });

  it("returns failed status with error message when result.subtype != success", async () => {
    claudeController.setScript({
      records: [
        systemInit("sess-fail"),
        resultError("sess-fail", "error_max_turns", "Max turns reached"),
      ],
    });
    const events: Array<Record<string, unknown>> = [];
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: (e) => {
        events.push(e as Record<string, unknown>);
      },
    });
    expect(result.status).toBe("failed");
    expect(result.error).toContain("Max turns reached");
    expect(events.find((e) => e.type === "error")).toBeDefined();
  });

  it("AbortSignal triggers cancelled status", async () => {
    claudeController.setScript({
      records: [systemInit("sess-abort")],
      delayMs: 50,
    });
    const ctrl = new AbortController();
    const backend = new ClaudeAgentBackend(CONFIG);
    const runPromise = backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      signal: ctrl.signal,
    });
    setTimeout(() => ctrl.abort(), 10);
    const result = await runPromise;
    expect(result.status).toBe("cancelled");
  });

  it("throws AgentBackendConfigError when apiKey missing", () => {
    expect(() => new ClaudeAgentBackend({ apiKey: "", model: "claude-sonnet-4-6" })).toThrow(
      /Anthropic API key/,
    );
  });

  it("throws AgentBackendConfigError when model is not a Claude model", () => {
    expect(() => new ClaudeAgentBackend({ apiKey: "k", model: "gemini-pro" })).toThrow(
      /Claude model/,
    );
  });

  it("forwards mcpServers to SDK options", async () => {
    claudeController.setScript({
      records: [systemInit("sess-mcp"), resultSuccess("sess-mcp", "ok")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    const mcp = { neko_ui: { command: "node", args: [] } };
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      mcpServers: mcp,
    });
    expect(claudeController.lastOptions()?.mcpServers).toBe(mcp);
  });

  it("wires outputSchema as outputFormat: { type: 'json_schema', schema }", async () => {
    claudeController.setScript({
      records: [systemInit("sess-out"), resultSuccess("sess-out", '{"ok":true}')],
    });
    const schema = { type: "object", properties: { ok: { type: "boolean" } } };
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      outputSchema: schema,
    });
    expect(claudeController.lastOptions()?.outputFormat).toEqual({
      type: "json_schema",
      schema,
    });
  });

  it("forwards forkSession when truthy", async () => {
    claudeController.setScript({
      records: [systemInit("sess-fork"), resultSuccess("sess-fork", "ok")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      forkSession: true,
      backendState: { "claude-agent": { sessionId: "prior" } },
    });
    expect(claudeController.lastOptions()?.forkSession).toBe(true);
    expect(claudeController.lastOptions()?.resume).toBe("prior");
  });

  it("forwards agents map", async () => {
    claudeController.setScript({
      records: [systemInit("sess-ag"), resultSuccess("sess-ag", "ok")],
    });
    const agents = {
      "card-renderer": { description: "renders cards", prompt: "you render cards" },
    };
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      agents,
    });
    expect(claudeController.lastOptions()?.agents).toBe(agents);
  });

  it("forwards onElicitation callback", async () => {
    claudeController.setScript({
      records: [systemInit("sess-el"), resultSuccess("sess-el", "ok")],
    });
    const onElicitation = async () => ({ action: "accept" as const });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      onElicitation,
    });
    expect(claudeController.lastOptions()?.onElicitation).toBe(onElicitation);
  });

  it("when canUseTool is set, forwards it and DROPS bypassPermissions", async () => {
    claudeController.setScript({
      records: [systemInit("sess-perm"), resultSuccess("sess-perm", "ok")],
    });
    const canUseTool = async () => ({ behavior: "allow" as const, updatedInput: {} });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      canUseTool,
    });
    const opts = claudeController.lastOptions()!;
    expect(opts.canUseTool).toBe(canUseTool);
    expect(opts.permissionMode).toBeUndefined();
    expect(opts.allowDangerouslySkipPermissions).toBeUndefined();
  });

  it("when canUseTool is NOT set, uses bypassPermissions", async () => {
    claudeController.setScript({
      records: [systemInit("sess-bp"), resultSuccess("sess-bp", "ok")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({ prompt: "p", workspace: FAKE_WORKSPACE, onEvent: () => {} });
    const opts = claudeController.lastOptions()!;
    expect(opts.permissionMode).toBe("bypassPermissions");
    expect(opts.allowDangerouslySkipPermissions).toBe(true);
    expect(opts.canUseTool).toBeUndefined();
  });

  it("merges caller hooks alongside internal PostToolUse", async () => {
    claudeController.setScript({
      records: [systemInit("sess-hooks"), resultSuccess("sess-hooks", "ok")],
    });
    const callerHook = vi.fn(async () => ({ continue: true }));
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "p",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
      hooks: { PreToolUse: [{ hooks: [callerHook] }] },
    });
    const merged = claudeController.lastOptions()?.hooks as Record<
      string,
      Array<{ hooks: unknown[] }>
    >;
    expect(merged.PreToolUse).toBeDefined();
    expect(merged.PreToolUse[0].hooks[0]).toBe(callerHook);
    expect(merged.PostToolUse).toBeDefined();
    expect(merged.PostToolUse[0].hooks).toHaveLength(1);
  });

  it("sets userMessage as SDK prompt and appends prompt to systemPrompt when both provided", async () => {
    claudeController.setScript({
      records: [systemInit("sess-um"), resultSuccess("sess-um", "ok")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({
      prompt: "system instructions + history",
      userMessage: "what is the weather?",
      workspace: FAKE_WORKSPACE,
      onEvent: () => {},
    });
    const call = claudeController.calls[0];
    expect(call.prompt).toBe("what is the weather?");
    expect(call.options.systemPrompt).toEqual({
      type: "preset",
      preset: "claude_code",
      append: "system instructions + history",
    });
  });

  it("outputSchema works in sync mode (no workspace)", async () => {
    claudeController.setScript({
      records: [systemInit("sess-out-sync"), resultSuccess("sess-out-sync", '{"ok":true}')],
    });
    const schema = { type: "object", properties: { ok: { type: "boolean" } } };
    const backend = new ClaudeAgentBackend(CONFIG);
    await backend.run({ prompt: "p", outputSchema: schema });
    expect(claudeController.lastOptions()?.outputFormat).toEqual({
      type: "json_schema",
      schema,
    });
  });

  it("sync mode (no onEvent) returns finalText raw without surface stripping", async () => {
    claudeController.setScript({
      records: [systemInit("sess-s"), resultSuccess("sess-s", "  hi  ")],
    });
    const backend = new ClaudeAgentBackend(CONFIG);
    const result = await backend.run({ prompt: "p" });
    expect(result.finalText).toBe("hi");
    expect(result.status).toBe("completed");
  });
});

describe("mergeHooks", () => {
  it("returns internal hooks when caller is undefined", () => {
    const internal = { PostToolUse: [{ hooks: [() => {}] }] };
    const merged = mergeHooks(undefined, internal);
    expect(merged.PostToolUse).toEqual(internal.PostToolUse);
  });

  it("concatenates caller matchers after internal ones for the same event", () => {
    const internalFn = () => {};
    const callerFn = () => {};
    const merged = mergeHooks(
      { PostToolUse: [{ hooks: [callerFn] }] },
      { PostToolUse: [{ hooks: [internalFn] }] },
    );
    expect(merged.PostToolUse).toHaveLength(2);
    expect(merged.PostToolUse[0].hooks[0]).toBe(internalFn);
    expect(merged.PostToolUse[1].hooks[0]).toBe(callerFn);
  });

  it("ignores caller entries that are not arrays", () => {
    const merged = mergeHooks(
      { PostToolUse: "not-an-array" as unknown as never },
      { PostToolUse: [{ hooks: [() => {}] }] },
    );
    expect(merged.PostToolUse).toHaveLength(1);
  });
});
