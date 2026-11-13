import { readFileSync, writeFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer, type Server, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { describe, expect, it } from "vitest";
import type { AgentBackend, AgentEvent, AgentRunOptions, AgentWorkspace } from "../src/agent-backend";
import { agentLimits, axLimitEnv, axRunLimits } from "../src/agent-limits";
import { AxBackend, axRunPrompt, normalizeAxUsage, type AxBackendConfig } from "../src/agent-backends/ax";
import { extractMarkdownText, outsideFenceText } from "../src/agent-backends/output";
import { extractCardFences, extractSurfaceMessages } from "../src/agent-backends/surface";
import { runAgentBackend } from "../src/work/agent-core";
import type { AgentControlPlane } from "../src/work/control-plane";
import type { PackActionDescriptor } from "../src/work/tools";

// The Go Ax backend and agent core (apps/openneko/internal/webfront/turnax.go)
// must send the same start frames, emit the same events and give the same
// results. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/axturn.json");

const SERVE_ENV = /^(OPENNEKO_MCP_(ORG_ID|THREAD_ID|SKILLS_ROOT)|OPENNEKO_HARNESS_[A-Z_]+|HARNESS_MODEL(_PROVIDER|_URL|_OPTIONS)?|HARNESS_MODEL_ROUTES|RUN_DIR|ARTIFACT_DIR|PYTHONPATH)$/;
const ENV_NAMES = [
  "MODEL_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AZURE_OPENAI_API_KEY", "FALLBACK_MODEL_API_KEY",
  "OPENNEKO_SANDBOX_URL", "OPENNEKO_SANDBOX_KEY", "OPENNEKO_AX_SKILL_RERANK", "OPENNEKO_AGENT_TURN_TIMEOUT_MS",
  "OPENNEKO_AGENT_MAX_TURNS", "OPENNEKO_AGENT_DELEGATION_MAX_ITERATIONS", "OPENNEKO_AGENT_TOOL_OUTPUT_MAX_BYTES",
  "OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS", "OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", "OPENNEKO_WORKFLOW_MAX_TOOL_CALLS",
  "OPENNEKO_WORKFLOW_MAX_CONTINUATIONS", "OPENNEKO_WORKFLOW_REASONING_EFFORT", "OPENNEKO_SERVE_URL", "OPENNEKO_MCP_BRIDGE",
];

function workspace(root: string): AgentWorkspace {
  return {
    orgRoot: `${root}/org`,
    skillsRoot: `${root}/org/skills`,
    memoryRoot: `${root}/org/memory`,
    knowledgeRoot: `${root}/org/knowledge`,
    uploadsRoot: `${root}/org/uploads`,
    runsRoot: `${root}/org/runs`,
    threadUploadsRoot: `${root}/org/uploads/thread-1`,
    runRoot: `${root}/org/runs/run-1`,
    artifactRoot: `${root}/org/runs/run-1/artifacts`,
    binRoot: `${root}/org/runs/run-1/bin`,
  };
}

const WS = workspace("/srv/neko");

type Frame = Record<string, unknown> | { raw: string } | "hangup";

type TurnCase = {
  name: string;
  config: AxBackendConfig;
  opts: Partial<AgentRunOptions> & { prompt: string };
  env?: Record<string, string>;
  scripts: Frame[][];
};

const ROUTE = { provider: "openai", model: "gpt-4.1", keyEnv: "OPENAI_API_KEY" };
const ev = (sequence: number, type: string, fields: Record<string, unknown> = {}) => ({
  type: "event",
  event: { version: 1, run_id: "workrun-1", sequence, type, ...fields },
});
const finished = (sequence: number, result: Record<string, unknown>) => ev(sequence, "run.finished", { result });
const exit = (code = 0, error = "") => ({ type: "exit", code, ...(error ? { error } : {}) });
const CARDS = '```neko_cards\n{"title":"Sales","table":"| Region | Total |\\n| --- | --- |\\n| East | 12 |"}\n```';
const BAD_CARDS = '```neko_cards\n{"title":""}\n```';
const A2UI = '```neko_a2ui\n[{"version":"v1.0","createSurface":{"surfaceId":"s1","catalogId":"urn:openneko:catalog:work:v2","components":[{"id":"root","component":"Markdown","text":"From the card"}]}}]\n```';
const PROMPT = "You are OpenNeko.\n\nRules.\n\n<workspace>\nroot\n</workspace>\n\nTurn part.";
const BASE_OPTS = {
  prompt: PROMPT,
  userMessage: "How are sales?",
  orgId: "org-1",
  workspace: WS,
  backendState: { compaction: { summary: "s" } },
  tag: "work run-1",
  wantsCards: true,
  mcpBridgeEnv: {
    OPENNEKO_MCP_MODE: "work",
    OPENNEKO_MCP_ORG_ID: "org-1",
    OPENNEKO_MCP_THREAD_ID: "thread-1",
    OPENNEKO_MCP_RUN_ID: "run-1",
    OPENNEKO_MCP_SKILLS_ROOT: WS.skillsRoot,
    OPENNEKO_MCP_WANTS_CARDS: "1",
    OPENNEKO_MCP_CARD_SCHEMA: "rich",
  },
};
const KEYS = { OPENAI_API_KEY: "sk-openai", MODEL_API_KEY: "sk-model" };

const TURNS: TurnCase[] = [
  {
    name: "answer with every event kind",
    config: { route: { provider: "anthropic", model: "claude-haiku-5-5", keyEnv: "ANTHROPIC_API_KEY" }, reasoningEffort: "medium" },
    opts: BASE_OPTS,
    env: { ANTHROPIC_API_KEY: "sk-ant" },
    scripts: [[
      ev(0, "run.started"),
      ev(1, "thought.delta", { data: { text: "Thinking about " } }),
      ev(2, "thought.delta", { data: { text: "sales." } }),
      ev(3, "actor.step", { data: { text: "  Look up sales  " } }),
      ev(4, "tool.started", { name: "graphjin_query", operation_id: 1, effect: "read", data: { query: "{ sales }" } }),
      ev(5, "tool.finished", { name: "graphjin_query", operation_id: 1, data: { rows: 3 } }),
      ev(6, "tool.started", { name: "memory_save", operation_id: 2, effect: "durable" }),
      ev(7, "tool.finished", { name: "memory_save", operation_id: 2, error: "denied" }),
      ev(8, "tool.started", { name: "mcp_neko_ui_render_cards", operation_id: 3, data: { title: "x" } }),
      ev(9, "tool.finished", { name: "mcp_neko_ui_render_cards", operation_id: 3 }),
      ev(10, "tool.started", { name: "mcp_neko_ui_render_cards", operation_id: 4 }),
      ev(11, "tool.finished", { name: "mcp_neko_ui_render_cards", operation_id: 4, error: "bad cards" }),
      ev(12, "skill.shortlist", { data: ["sales-report", 3, "pdf"] }),
      ev(13, "skill.shortlist", { error: "index down" }),
      ev(14, "skill.loaded", { name: "sales-report" }),
      ev(15, "skill.used", { name: "sales-report" }),
      ev(16, "actor.code", { data: { code: "x()" } }),
      ev(17, "model.route.fallback"),
      ev(18, "executor.step.failed"),
      ev(19, "model.request.finished", { observed_model: "claude-haiku-5-5-20261001", provider: "anthropic" }),
      ev(20, "answer.delta", { data: { version: 1, text: "Sales are " } }),
      ev(21, "answer.delta", { data: { version: 1, text: "up.\n```neko_wo" } }),
      ev(22, "answer.delta", { data: { version: 1, text: "rkflow_save\n{}\n```\nMore" } }),
      ev(23, "answer.delta", { data: { text: 7 } }),
      ev(24, "unknown.kind"),
      finished(25, {
        status: "completed", kind: "answer",
        answer: `Sales are up.\n\`\`\`neko_workflow_save\n{}\n\`\`\`\nMore text.\n\n${CARDS}\n\n${BAD_CARDS}`,
        usage: { requests: 3, reported: 2, input_tokens: 150000, output_tokens: 2000, total_tokens: 152500, cache_read_tokens: 100, coverage: "partial" },
      }),
      exit(),
    ]],
  },
  {
    name: "a2ui answer with debug, fallback and limits",
    config: {
      route: { provider: "azure-openai", model: "gpt-4.1", keyEnv: "AZURE_OPENAI_API_KEY", options: { resource_name: "r", deployment_name: "gpt-4.1", api_version: "2024-10-21" }, price: { inputUsdPer1M: 2, outputUsdPer1M: 8 } },
      fallback: { provider: "openai-compatible", model: "llama", url: "http://models.local/v1" },
      contextWindowTokens: 128000,
      maxOutputTokens: 4096,
      reasoningEffort: "high",
    },
    opts: { ...BASE_OPTS, debug: true, nativeDelegation: "disabled", networkHosts: ["api.example.com", "docs.example.com"], timeoutMs: 2_000_000, maxToolIterations: 40, reasoningEffort: "low" },
    env: { AZURE_OPENAI_API_KEY: "sk-az", FALLBACK_MODEL_API_KEY: "sk-fb", OPENNEKO_SANDBOX_URL: "http://sandbox.local", OPENNEKO_AX_SKILL_RERANK: "off", OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS: "90", OPENNEKO_AGENT_MAX_TURNS: "900" },
    scripts: [[
      ev(1, "actor.code", { data: { code: "final()", error: "SyntaxError" } }),
      ev(2, "thought.delta", { data: { text: "hidden for azure" } }),
      ev(3, "answer.delta", { data: { version: 1, text: "Old text" } }),
      ev(4, "answer.delta", { data: { version: 2, text: "New" } }),
      finished(5, { status: "completed", kind: "summary", code: "max_steps", answer: `Intro.\n${A2UI}`, usage: { requests: 2, reported: 2, input_tokens: 1000, output_tokens: 500, total_tokens: 1600, coverage: "complete", cost_micros: 999, priced_by: "catalog", catalog_version: "v9" } }),
      exit(),
    ]],
  },
  {
    name: "cards off and no stream text",
    config: { route: ROUTE },
    opts: { ...BASE_OPTS, wantsCards: false, userMessage: undefined, workspace: { ...WS, threadUploadsRoot: "" } },
    env: KEYS,
    scripts: [[
      finished(1, { status: "completed", answer: `Plain.\n${CARDS}\n`, usage: { requests: 1, reported: 1, input_tokens: 10, output_tokens: 5, total_tokens: 15, coverage: "complete", cost_micros: 12, priced_by: "catalog", catalog_version: "v9" } }),
      exit(),
    ]],
  },
  {
    name: "deadline exceeded",
    config: { route: ROUTE },
    opts: { ...BASE_OPTS, timeoutMs: 61_000 },
    env: { MODEL_API_KEY: "sk-model" },
    scripts: [[finished(1, { status: "failed", kind: "failure", code: "deadline_exceeded", usage: { coverage: "unavailable" } }), exit(1, "turn timed out")]],
  },
  {
    name: "failure with diagnostics",
    config: { route: { provider: "openai-compatible", model: "unpriced-model", url: "http://m.local/v1" } },
    opts: BASE_OPTS,
    scripts: [[
      { raw: "not json" },
      ev(1, "answer.delta", { data: { version: 1, text: "Partial" } }),
      finished(2, { status: "failed", code: "provider_error", answer: "" }),
      exit(1, "provider said no"),
    ]],
  },
  {
    name: "exit without a result",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [[ev(1, "run.started"), exit(2, "bad spec")]],
  },
  {
    name: "events out of order",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [[
      ev(2, "actor.step", { data: { text: "a" } }),
      ev(2, "actor.step", { data: { text: "b" } }),
      { type: "event", event: { version: 2, run_id: "workrun-1", sequence: 3, type: "actor.step" } },
      { type: "event", event: { version: 1, run_id: "other", sequence: 4, type: "actor.step" } },
      exit(0),
    ]],
  },
  {
    name: "malformed call retried",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [
      [ev(1, "tool.started", { name: "graphjin_query", operation_id: 1, effect: "read" }), finished(2, { status: "failed", code: "MALFORMED_FUNCTION_CALL" }), exit(1)],
      [ev(1, "answer.delta", { data: { version: 1, text: "Second try." } }), finished(2, { status: "completed", answer: "Second try." }), exit()],
    ],
  },
  {
    name: "malformed call after a write is not retried",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [[ev(1, "tool.started", { name: "memory_save", operation_id: 1, effect: "durable" }), finished(2, { status: "failed", code: "MALFORMED_FUNCTION_CALL" }), exit(1)]],
  },
  {
    name: "cancelled by the harness",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [[finished(1, { status: "cancelled" }), exit(0)]],
  },
  {
    name: "connection closed without exit",
    config: { route: ROUTE },
    opts: BASE_OPTS,
    env: KEYS,
    scripts: [[ev(1, "run.started"), "hangup"]],
  },
  {
    name: "empty answer",
    config: { route: ROUTE },
    opts: { ...BASE_OPTS, workspace: undefined, tag: "x y!" },
    env: KEYS,
    scripts: [[{ type: "event", event: { version: 1, run_id: "xy", sequence: 1, type: "run.finished", result: { status: "completed", answer: "   " } } }, exit()]],
  },
];

async function withServe<T>(scripts: Frame[][], fn: (socket: string, starts: unknown[]) => Promise<T>): Promise<T> {
  const dir = await mkdtemp(join(tmpdir(), "ax-fixture-"));
  const socket = join(dir, "serve.sock");
  const starts: unknown[] = [];
  const queue = [...scripts];
  const server: Server = createServer((conn: Socket) => {
    const script = queue.shift() ?? [exit(9, "no script")];
    const reader = createInterface({ input: conn });
    let started = false;
    reader.on("line", (line) => {
      if (started) return;
      started = true;
      starts.push(JSON.parse(line));
      for (const frame of script) {
        if (frame === "hangup") {
          conn.end();
          return;
        }
        conn.write(`${"raw" in frame ? frame.raw : JSON.stringify(frame)}\n`);
      }
      conn.end();
    });
    conn.on("error", () => {});
  });
  await new Promise<void>((done) => server.listen(socket, done));
  try {
    return await fn(socket, starts);
  } finally {
    await new Promise<void>((done) => server.close(() => done()));
    await rm(dir, { recursive: true, force: true });
  }
}

const SCRATCH = new RegExp(`${join(tmpdir(), "neko-ax-")}[A-Za-z0-9]+`, "g");

function sortedStart(start: unknown): unknown {
  const s = start as { environ?: string[] };
  return { ...s, environ: [...(s.environ ?? [])].map((e) => e.replace(SCRATCH, "SCRATCH")).sort() };
}

const UUID = /answer-[0-9a-f-]{36}/g;

async function runTurn(c: TurnCase) {
  const saved = { ...process.env };
  for (const name of Object.keys(process.env)) {
    if (SERVE_ENV.test(name) || ENV_NAMES.includes(name)) delete process.env[name];
  }
  if (c.opts.workspace) {
    process.env.RUN_DIR = c.opts.workspace.runRoot;
    process.env.ARTIFACT_DIR = c.opts.workspace.artifactRoot;
    process.env.PYTHONPATH = c.opts.workspace.binRoot;
  }
  Object.assign(process.env, c.env ?? {});
  try {
    return await withServe(c.scripts, async (socket, starts) => {
      process.env.OPENNEKO_SERVE_SOCKET = socket;
      const events: AgentEvent[] = [];
      const result = await new AxBackend(c.config).run({
        ...c.opts,
        onEvent: async (event) => {
          events.push(event);
        },
      } as AgentRunOptions);
      return {
        name: c.name,
        config: c.config,
        opts: c.opts,
        env: c.env ?? {},
        scripts: c.scripts,
        starts: starts.map(sortedStart),
        events: JSON.parse(JSON.stringify(events).replace(UUID, "answer-ID")),
        result,
      };
    });
  } finally {
    for (const name of Object.keys(process.env)) delete process.env[name];
    Object.assign(process.env, saved);
  }
}

const USAGE_ROUTE = { provider: "anthropic", model: "claude-haiku-5-5" };
const USAGES = [
  { name: "none", usage: undefined, route: USAGE_ROUTE },
  { name: "complete catalog", usage: { requests: 1, reported: 1, input_tokens: 1000, output_tokens: 200, total_tokens: 1300, cache_read_tokens: 50, cache_write_tokens: 25, reasoning_tokens: 25, coverage: "complete" }, route: USAGE_ROUTE },
  { name: "long context per call", usage: { requests: 1, reported: 1, input_tokens: 150000, output_tokens: 10, total_tokens: 150010, coverage: "complete" }, route: USAGE_ROUTE },
  { name: "long context spread over calls", usage: { requests: 2, reported: 2, input_tokens: 150000, output_tokens: 10, total_tokens: 150010, coverage: "complete" }, route: USAGE_ROUTE },
  { name: "partial scaled", usage: { requests: 4, reported: 1, input_tokens: 100, output_tokens: 10, total_tokens: 110, coverage: "partial" }, route: USAGE_ROUTE },
  { name: "partial with no report", usage: { requests: 4, reported: 0, input_tokens: 100, coverage: "partial" }, route: USAGE_ROUTE },
  { name: "harness priced", usage: { requests: 1, reported: 1, input_tokens: 100, output_tokens: 10, total_tokens: 110, coverage: "complete", cost_micros: 1234, priced_by: "catalog", catalog_version: "v1.2" }, route: USAGE_ROUTE },
  { name: "harness priced zero", usage: { requests: 1, reported: 1, coverage: "complete", cost_micros: 0, priced_by: "catalog", catalog_version: "v1.2" }, route: USAGE_ROUTE },
  { name: "harness priced without version", usage: { requests: 1, reported: 1, input_tokens: 100, coverage: "complete", cost_micros: 5, priced_by: "catalog" }, route: USAGE_ROUTE },
  { name: "record price wins", usage: { requests: 1, reported: 1, input_tokens: 1000, output_tokens: 100, total_tokens: 1200, cache_read_tokens: 100, coverage: "complete", cost_micros: 5, priced_by: "catalog", catalog_version: "v1" }, route: { ...USAGE_ROUTE, price: { inputUsdPer1M: 1, outputUsdPer1M: 4 } } },
  { name: "unpriced model", usage: { requests: 1, reported: 1, input_tokens: 100, coverage: "complete" }, route: { provider: "openai-compatible", model: "local" } },
  { name: "unavailable", usage: { coverage: "unavailable" }, route: USAGE_ROUTE },
  { name: "no coverage", usage: { input_tokens: 3 }, route: USAGE_ROUTE },
];

const PROMPTS = [
  { prompt: PROMPT, userMessage: "Hi" },
  { prompt: PROMPT, userMessage: undefined },
  { prompt: "No turn sections here.", userMessage: "Q" },
  { prompt: "<workspace>\nat the start\n</workspace>", userMessage: "Q" },
  { prompt: "Org.\n\n<operator-profile>\np\n</operator-profile>\n\n<bot>\nb\n</bot>", userMessage: "Q" },
  { prompt: "Org.\n\n<files>\nf\n</files>\n\n<long_term_memory>\nm\n</long_term_memory>", userMessage: "Q" },
];

const OUTSIDE = [
  "plain text",
  "before ```neko_a2ui\n[]\n``` after",
  "open ```neko_cards\n{} and never closed",
  "tail ```neko_wo",
  "tail ```",
  "tail `",
  "two ```neko_rule_save\n{}\n```mid```neko_action_request\nx\n```end",
  "```neko_workflow_output\nx\n```",
  "code ```js\nx\n``` stays",
];

const CARD_TEXTS = [
  "No cards here.",
  `Intro.\n\n${CARDS}\n\n\n\nOutro.`,
  `${BAD_CARDS}`,
  "```neko_cards\nnot json\n```",
  `${CARDS}\n${CARDS}`,
];

const SURFACES = [
  "no fence",
  `Before\n${A2UI}\nAfter`,
  "```neko_a2ui\n{\"a\":1}\n```",
  "```neko_a2ui\n<Card>Some <b>prose</b></Card>\n```",
  "```neko_a2ui\n<Card></Card>\n```",
  "```neko_a2ui\n[{\"version\":\"v1.0\",\"deleteSurface\":{\"surfaceId\":\"s\"},\"extra\":1},{\"version\":\"v1.0\",\"deleteSurface\":{\"surfaceId\":\"s\",\"extra\":1}},{\"version\":\"v2\",\"deleteSurface\":{\"surfaceId\":\"s\"}},{\"version\":\"v1.0\",\"deleteSurface\":{\"surfaceId\":\"kept\"}}]\n```",
  "```neko_a2ui\n[{\"version\":\"v0.9\",\"createSurface\":{\"surfaceId\":\"s\",\"catalogId\":\"c\"}},{\"version\":\"v1.0\",\"bad\":1},{\"version\":\"v1.0\",\"updateComponents\":{\"surfaceId\":\"s\",\"components\":[{\"id\":\"a\",\"component\":\"Markdown\",\"text\":\"t\",\"extra\":1}]}},{\"version\":\"v1.0\",\"updateDataModel\":{\"surfaceId\":\"s\",\"path\":\"/x\",\"value\":[1]}},{\"version\":\"v1.0\",\"deleteSurface\":{\"surfaceId\":\"s\"}},{\"version\":\"v1.0\",\"deleteSurface\":{\"surfaceId\":\"\"}},{\"version\":\"v1.0\",\"createSurface\":{\"surfaceId\":\"s\",\"catalogId\":\"c\",\"components\":[]}}]\n```",
];

const MARKDOWN = [
  [],
  [{ version: "v1.0", createSurface: { surfaceId: "s", catalogId: "c", components: [{ id: "a", component: "Markdown", text: " One " }, { id: "b", component: "Text", text: "skip" }, { id: "c", component: "Column", children: [{ component: "Markdown", text: "Two" }] }] } }],
  [{ component: "Markdown", text: 5 }],
];

const DESCRIPTORS: PackActionDescriptor[] = [
  { kind: "send_slack", label: "Send Slack", description: "Post", default_mode: "approval_required", scope: "external", input_schema: { type: "object", properties: {} } } as unknown as PackActionDescriptor,
];
const DENIED: PackActionDescriptor[] = [
  { kind: "drop_table", label: "Drop", description: "Drop", default_mode: "never" } as unknown as PackActionDescriptor,
];

const CORES = [
  { name: "web admin", wantsCards: true, cardSchema: "rich" as const, sourceConfigEnabled: true, packActions: DESCRIPTORS, filesUrl: "http://serve.local/" },
  { name: "telegram member", wantsCards: false, cardSchema: "core" as const, sourceConfigEnabled: false, packActions: DENIED, nativeDelegation: "disabled" as const, graphjinToolPolicy: { allowedTools: ["query"] } },
  { name: "defaults", packActions: [] },
];

async function coreRun(c: (typeof CORES)[number]) {
  const saved = process.env.OPENNEKO_SERVE_URL;
  if (c.filesUrl) process.env.OPENNEKO_SERVE_URL = c.filesUrl;
  else delete process.env.OPENNEKO_SERVE_URL;
  let seen: AgentRunOptions | undefined;
  const backend = {
    id: "ax",
    capabilities: { mcpTools: true, sessionResume: false },
    run: async (opts: AgentRunOptions) => {
      seen = opts;
      return { finalText: "", status: "completed" };
    },
  } as unknown as AgentBackend;
  try {
    await runAgentBackend({
      backend,
      prompt: "p",
      userMessage: "m",
      orgId: "org-1",
      threadId: "thread-1",
      runId: "run-1",
      workspace: WS,
      controlPlane: {} as AgentControlPlane,
      emit: async () => {},
      ...(c.wantsCards !== undefined ? { wantsCards: c.wantsCards } : {}),
      ...(c.cardSchema ? { cardSchema: c.cardSchema } : {}),
      ...(c.sourceConfigEnabled !== undefined ? { sourceConfigEnabled: c.sourceConfigEnabled } : {}),
      ...(c.nativeDelegation ? { nativeDelegation: c.nativeDelegation } : {}),
      ...(c.graphjinToolPolicy ? { graphjinToolPolicy: c.graphjinToolPolicy as never } : {}),
      packActions: c.packActions,
    });
  } finally {
    if (saved === undefined) delete process.env.OPENNEKO_SERVE_URL;
    else process.env.OPENNEKO_SERVE_URL = saved;
  }
  const servers = Object.keys(seen!.mcpServers ?? {}).filter(
    (name) => /^neko_[a-z0-9_]+$/.test(name) && (name !== "neko_ui" || seen!.wantsCards),
  );
  return { ...c, servers, bridgeEnv: seen!.mcpBridgeEnv, wantsCards: seen!.wantsCards, tag: seen!.tag, nativeDelegation: seen!.nativeDelegation ?? null };
}

describe("Ax turn fixtures for the Go agent core", () => {
  it("matches the fixture the Go test reads", async () => {
    const turns = [];
    for (const c of TURNS) turns.push(await runTurn(c));
    const cores = [];
    for (const c of CORES) cores.push(await coreRun(c));
    const saved = { ...process.env };
    for (const name of ENV_NAMES) delete process.env[name];
    const limits = [
      { kind: "chat", env: {} },
      { kind: "workflow", env: {} },
      { kind: "agent-job", env: {} },
      { kind: "workflow", env: { OPENNEKO_WORKFLOW_REASONING_EFFORT: "high", OPENNEKO_WORKFLOW_MAX_TOOL_CALLS: "900", OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS: "12.7", OPENNEKO_AGENT_DELEGATION_MAX_ITERATIONS: "700" } },
      { kind: "chat", env: { OPENNEKO_AGENT_TURN_TIMEOUT_MS: "abc", OPENNEKO_AGENT_MAX_TURNS: "0", OPENNEKO_AGENT_TOOL_OUTPUT_MAX_BYTES: " 2048 ", OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS: "601" } },
      { kind: "workflow", env: { OPENNEKO_WORKFLOW_REASONING_EFFORT: "max" } },
    ].map((c) => {
      for (const name of ENV_NAMES) delete process.env[name];
      Object.assign(process.env, c.env);
      const l = agentLimits(c.kind as "chat");
      return { ...c, limits: l, spec: axRunLimits(l), env2: axLimitEnv(l) };
    });
    for (const name of Object.keys(process.env)) delete process.env[name];
    Object.assign(process.env, saved);
    const fixture = `${JSON.stringify(
      {
        turns,
        cores,
        limits,
        usages: USAGES.map((c) => ({ ...c, out: normalizeAxUsage(c.usage as never, c.route) })),
        prompts: PROMPTS.map((c) => ({ ...c, out: axRunPrompt(c.prompt, c.userMessage) })),
        outside: OUTSIDE.map((raw) => ({ raw, out: outsideFenceText(raw) })),
        cards: CARD_TEXTS.flatMap((raw) => [false, true].map((rich) => ({ raw, rich, out: JSON.parse(JSON.stringify(extractCardFences(raw, { rich })).replace(UUID, "answer-ID")) }))),
        surfaces: SURFACES.map((raw) => ({ raw, out: extractSurfaceMessages(raw) })),
        markdown: MARKDOWN.map((messages) => ({ messages, out: extractMarkdownText(messages as never) })),
      },
      null,
      1,
    )}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  }, 60_000);
});
