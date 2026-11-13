import { createHash } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { mkdir, mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentBackend, AgentEvent, AgentRunResult, AgentWorkspace } from "../src/agent-backend";
import type { KnowledgePackContents } from "../src/knowledge-pack";
import type { AgentControlPlane } from "../src/work/control-plane";
import { runWorkflowAgentBackend } from "../src/workflows/agent-core";
import { buildWorkflowRunnerPrompt, buildWorkflowSeedMessage } from "../src/workflows/runner-prompt";
import { expandArg, formatScriptStepResults, runScriptSteps, type ScriptStepResult } from "../src/workflows/script-steps";
import type { WorkflowRecord, WorkflowStep } from "../src/workflows/store";

// The Go workflow turn (apps/openneko/internal/webfront/workflowturn.go) must
// build the same prompt, run the script steps and continue a timed-out turn
// as Node does. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/workflowturn.json");

const workspace: AgentWorkspace = {
  orgRoot: "/home/neko/.config/openneko/agents/orgs/org_1",
  skillsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/skills",
  memoryRoot: "/home/neko/.config/openneko/agents/orgs/org_1/memory",
  knowledgeRoot: "/home/neko/.config/openneko/agents/orgs/org_1/knowledge",
  uploadsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/uploads",
  runsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs",
  threadUploadsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/uploads/t-1",
  runRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1",
  artifactRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1/artifacts",
  binRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1/bin",
};

const agentic: KnowledgePackContents = {
  mode: "agentic",
  tables: JSON.stringify({ tables: [{ schema: "sales", name: "orders", column_count: 9, summary: "Orders 🚚" }] }),
  namespaces: "{}",
  insights: JSON.stringify({
    hub_tables: [{ name: "orders", summary: "Orders", join_paths: ["orders -> customer"], examples: ["query { orders { id } }"] }],
    api_operations: [{ name: "listOrders", summary: "List orders" }],
    help_cards: [{ id: "help:filters", summary: "Filters" }],
  }),
  syntax: '{"operators":["eq"]}',
};

const legacy: KnowledgePackContents = { mode: "legacy", tables: "{\"tables\":[]}", namespaces: "{}", insights: "{}", syntax: "{}" };

const STEPS: unknown[] = [
  { id: "s1", description: "Read low stock" },
  { id: "s2", description: "Build the sheet", script: { skill: "xlsx", command: ["python3", "${SKILL_DIR}/run.py"] } },
  { id: "s3", description: "Mail the buyer ₹ \"now\"" },
  { id: "s4" },
];

type PromptInput = {
  backend: string;
  name: string;
  description: string;
  goal: string;
  systemPromptOverlay: string;
  steps: unknown[];
  mode: "live" | "headless";
  includeUxMetadata: boolean;
  memoryContext: string;
  workspace: AgentWorkspace;
  knowledge: KnowledgePackContents;
  filesEnabled: boolean;
  scriptData: boolean;
};

const base: PromptInput = {
  backend: "ax",
  name: "Low stock watch",
  description: "Flags SKUs below their reorder point.",
  goal: "",
  systemPromptOverlay: "",
  steps: STEPS,
  mode: "headless",
  includeUxMetadata: true,
  memoryContext: "",
  workspace,
  knowledge: agentic,
  filesEnabled: false,
  scriptData: true,
};

const PROMPTS: Array<{ name: string; input: PromptInput }> = [
  { name: "headless with steps", input: base },
  {
    name: "live with goal, overlay, memory and files",
    input: {
      ...base,
      mode: "live",
      goal: "  Keep stock above the floor.  ",
      systemPromptOverlay: "\n Never email suppliers on Sunday. ${name}\n",
      memoryContext: "Memories the operator has saved — treat as durable context:\n- [m1] business_rule (global): Fiscal year starts in April.",
      filesEnabled: true,
    },
  },
  { name: "eval without value block", input: { ...base, includeUxMetadata: false, description: "", knowledge: legacy, steps: [] } },
  { name: "other backend", input: { ...base, backend: "other", goal: " \n ", systemPromptOverlay: "  ", memoryContext: "  \n" } },
  { name: "no sandbox, so no script data", input: { ...base, scriptData: false } },
];

function workflowOf(input: PromptInput): WorkflowRecord {
  return {
    name: input.name,
    description: input.description,
    goal: input.goal,
    systemPromptOverlay: input.systemPromptOverlay,
    steps: input.steps as WorkflowStep[],
  } as WorkflowRecord;
}

function withFiles<T>(on: boolean, fn: () => T): T {
  const before = process.env.OPENNEKO_SERVE_URL;
  if (on) process.env.OPENNEKO_SERVE_URL = "http://serve.test";
  else delete process.env.OPENNEKO_SERVE_URL;
  try {
    return fn();
  } finally {
    if (before === undefined) delete process.env.OPENNEKO_SERVE_URL;
    else process.env.OPENNEKO_SERVE_URL = before;
  }
}

const SEEDS: Array<{ name: string; triggerKind: "manual" | "cron" | "subscription" | "watcher" | "api"; userMessage: string; payload: unknown }> = [
  { name: "Low stock", triggerKind: "manual", userMessage: "", payload: null },
  { name: "Low stock", triggerKind: "manual", userMessage: "  Run it for the north store  ", payload: { store: "north" } },
  { name: "Low stock", triggerKind: "manual", userMessage: " \n ", payload: {} },
  { name: "Nightly \"sync\"", triggerKind: "cron", userMessage: "", payload: {} },
  { name: "Order watch", triggerKind: "subscription", userMessage: "", payload: { row: { id: 7, total: 12.5, tags: ["a", "b"], note: null, nested: { empty: {}, list: [] } }, "<b>": "x&y" } },
  { name: "Order watch", triggerKind: "watcher", userMessage: "", payload: { value: 3 } },
  { name: "API flow", triggerKind: "api", userMessage: "", payload: { input: "€".repeat(9000) } },
  { name: "API flow", triggerKind: "api", userMessage: "", payload: [] },
  { name: "API flow", triggerKind: "api", userMessage: "", payload: [1, "two"] },
];

const VARS = { SKILL_DIR: "/w/skills/xlsx", RUN_DIR: "/w/runs/r1", ARTIFACT_DIR: "/w/runs/r1/artifacts" };
const EXPANDS: Array<{ arg: string; input: Record<string, unknown> }> = [
  { arg: "plain", input: {} },
  { arg: "${SKILL_DIR}/run.py", input: {} },
  { arg: "--out=${ARTIFACT_DIR}/a.csv;${RUN_DIR}", input: {} },
  { arg: "${input.store}", input: { store: "north" } },
  { arg: "${input.count}", input: { count: 0 } },
  { arg: "${input.row}", input: { row: { id: 7, tags: ["a"] } } },
  { arg: "${input.flag}-${input.n}", input: { flag: false, n: 2.5 } },
  { arg: "${input.missing:-fallback}", input: {} },
  { arg: "${input.empty:-}", input: { empty: "" } },
  { arg: "${input.empty:-none}", input: { empty: "" } },
  { arg: "${input.nul:-x}", input: { nul: null } },
  { arg: "${input.missing}", input: {} },
  { arg: "${HOME}", input: {} },
  { arg: "${lower} ${input.} ${A:-b}", input: {} },
  { arg: "$SKILL_DIR ${SKILL_DIR:-other}", input: {} },
];

const RESULTS: ScriptStepResult[][] = [
  [],
  [
    { id: "s1", description: "Pull data", status: "ok", exitCode: 0, durationMs: 1499, outputPath: "/w/runs/r1/steps/s1.output.log", outputTail: "rows: 12" },
    { id: "s2", description: "Build", status: "failed", exitCode: 3, durationMs: 2500, outputPath: "/w/runs/r1/steps/s2.output.log", outputTail: "Traceback\nValueError", error: undefined },
    { id: "s3", description: "Mail", status: "timeout", exitCode: null, durationMs: 600_000, outputPath: "/w/runs/r1/steps/s3.output.log", error: "timed out after 600s" },
    { id: "s4", description: "Skipped", status: "skipped", exitCode: null, durationMs: 0, error: "skipped because step s2 did not succeed" },
  ],
];

type Turn = { events: AgentEvent[]; result: AgentRunResult };
const TIMEOUT_ERROR = "ax turn exceeded its 900s budget and was terminated (OPENNEKO_AGENT_TURN_TIMEOUT_MS overrides)";
const longText = `${"Checked the orders table. ".repeat(45)}Reorder 🚚 SKU-1.`;
const toolCalls: AgentEvent[] = Array.from({ length: 17 }, (_, i) => ({
  type: "tool_start",
  id: `t${i}`,
  name: "mcp_neko_graphjin_execute_graphql",
  input: { query: `{ orders(limit: ${i}) { id } }`, ...(i === 3 ? { note: "x".repeat(400) } : {}) },
}) as AgentEvent);
const timedOut = (events: AgentEvent[]): Turn => ({
  events: [...events, { type: "error", message: TIMEOUT_ERROR }],
  result: { finalText: "", status: "failed", error: TIMEOUT_ERROR, timedOut: true },
});
const done = (text: string): Turn => ({
  events: [{ type: "message", role: "assistant", content: text }],
  result: { finalText: text, status: "completed" },
});

const CONTINUATIONS: Array<{ name: string; maxContinuations: number; turns: Turn[] }> = [
  { name: "completes at once", maxContinuations: 2, turns: [done("All stock is fine.")] },
  {
    name: "times out, then completes",
    maxContinuations: 2,
    turns: [
      timedOut([
        ...toolCalls,
        { type: "tool_start", id: "u", name: "terminal" } as AgentEvent,
        { type: "message", role: "assistant", content: longText },
        { type: "message", role: "user", content: "ignored" },
        { type: "status", message: "still working" },
      ]),
      done("Reordered."),
    ],
  },
  {
    name: "runs out of continuations",
    maxContinuations: 2,
    turns: [
      timedOut([{ type: "message", role: "assistant", content: "  " }]),
      timedOut([toolCalls[0]!]),
      timedOut([{ type: "message", role: "assistant", content: "Halfway." }]),
    ],
  },
  { name: "no continuations allowed", maxContinuations: 0, turns: [timedOut([toolCalls[1]!])] },
  {
    name: "a failure that is no timeout",
    maxContinuations: 2,
    turns: [{
      events: [{ type: "error", message: "model said no" }],
      result: { finalText: "", status: "failed", error: "model said no" },
    }],
  },
];

function fakeBackend(turns: Turn[]): { backend: AgentBackend; prompts: string[]; servers: string[][] } {
  const queue = [...turns];
  const prompts: string[] = [];
  const servers: string[][] = [];
  const backend = {
    id: "ax",
    configuredIdentity: "ax",
    capabilities: { mcpTools: true },
    async run(opts: { prompt: string; mcpServers?: Record<string, unknown>; onEvent?: (e: AgentEvent) => Promise<void> }) {
      prompts.push(opts.prompt);
      servers.push(Object.keys(opts.mcpServers ?? {}));
      const turn = queue.shift();
      if (!turn) throw new Error("no turn left");
      for (const event of turn.events) await opts.onEvent?.(event);
      return turn.result;
    },
  } as unknown as AgentBackend;
  return { backend, prompts, servers };
}

async function runCore(turns: Turn[], maxContinuations: number, files = false) {
  const { backend, prompts, servers } = fakeBackend(turns);
  const events: AgentEvent[] = [];
  const before = process.env.OPENNEKO_SERVE_URL;
  if (files) process.env.OPENNEKO_SERVE_URL = "http://serve.test";
  else delete process.env.OPENNEKO_SERVE_URL;
  try {
    const result = await runWorkflowAgentBackend({
      backend,
      prompt: "BASE PROMPT",
      userMessage: "Begin.",
      orgId: "org_1",
      threadId: "thread_1",
      runId: "run_1",
      workflowRunId: "wfr_1",
      mode: "headless",
      networkHosts: [],
      workspace,
      controlPlane: {} as AgentControlPlane,
      emit: async (event) => { events.push(event); },
      maxContinuations,
    });
    return { result, events, prompts, servers };
  } finally {
    if (before === undefined) delete process.env.OPENNEKO_SERVE_URL;
    else process.env.OPENNEKO_SERVE_URL = before;
  }
}

type SandboxAnswer = { exit_code: number; output: string; timed_out?: boolean; changed?: Array<{ path: string; mode: number; data: string }> };
type StepScenario = { name: string; sandbox: boolean; allowed: string[]; input: Record<string, unknown>; steps: unknown[]; answers: SandboxAnswer[] };

const SCRIPT = { skill: "xlsx", command: ["python3", "${SKILL_DIR}/run.py", "--out", "${ARTIFACT_DIR}/stock.csv", "--store", "${input.store}", "--n", "${input.count:-5}", "it's"], timeoutSeconds: 30 };
const STEP_SCENARIOS: StepScenario[] = [
  { name: "sandbox off", sandbox: false, allowed: ["xlsx"], input: {}, steps: [{ id: "a", description: "A", script: SCRIPT }, { id: "b", description: "B", script: SCRIPT }], answers: [] },
  {
    name: "runs, then a missing input stops the rest",
    sandbox: true,
    allowed: ["xlsx", "pdf"],
    input: { store: "north" },
    steps: [
      { id: "note", description: "No script" },
      { id: "pull data/1", description: "Pull", script: SCRIPT },
      { id: "b", description: "Needs input", script: { skill: "xlsx", command: ["sh", "-c", "echo ${input.missing}"] } },
      { id: "c", description: "Never runs", script: { skill: "pdf", command: ["true"] } },
    ],
    answers: [{
      exit_code: 0,
      output: `${"é".repeat(2500)}\nrows: 123\n`,
      changed: [{ path: "runs/r1/artifacts/stock.csv", mode: 0o644, data: Buffer.from("sku,qty\nA,1\n").toString("base64") }],
    }],
  },
  {
    name: "skill out of scope",
    sandbox: true,
    allowed: ["pdf"],
    input: {},
    steps: [{ id: "a", description: "A", script: SCRIPT }],
    answers: [],
  },
  {
    name: "skill not installed",
    sandbox: true,
    allowed: ["docx"],
    input: {},
    steps: [{ id: "a", description: "A", script: { skill: "docx", command: ["true"] } }],
    answers: [],
  },
  {
    name: "timeout and non-zero exit",
    sandbox: true,
    allowed: ["xlsx"],
    input: { store: "south", count: 2 },
    steps: [{ id: "t", description: "Slow", script: SCRIPT }],
    answers: [{ exit_code: -1, output: "partial\n", timed_out: true }],
  },
  {
    name: "non-zero exit",
    sandbox: true,
    allowed: ["xlsx"],
    input: { store: "south" },
    steps: [{ id: "x", description: "Fails", script: { skill: "xlsx", command: ["python3", "${SKILL_DIR}/run.py"] } }, { id: "y", description: "Next", script: SCRIPT }],
    answers: [{ exit_code: 2, output: "Traceback\nValueError: bad\n" }],
  },
];

type Seen = { command: string; dir?: string; skill_dir?: string; env?: Record<string, string>; timeout_seconds: number; max_output: number; fresh: boolean; put: string[] };

async function fakeSandbox(answers: SandboxAnswer[]): Promise<{ url: string; seen: Seen[]; ended: () => number; close: () => Promise<void> }> {
  const queue = [...answers];
  const seen: Seen[] = [];
  let ended = 0;
  const server: Server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      if (req.method === "DELETE") {
        ended++;
        res.writeHead(204).end();
        return;
      }
      const parsed = JSON.parse(body) as Seen & { put?: Array<{ path: string }> };
      seen.push({
        command: parsed.command,
        ...(parsed.dir ? { dir: parsed.dir } : {}),
        ...(parsed.skill_dir ? { skill_dir: parsed.skill_dir } : {}),
        ...(parsed.env ? { env: parsed.env } : {}),
        timeout_seconds: parsed.timeout_seconds,
        max_output: parsed.max_output,
        fresh: parsed.fresh,
        put: (parsed.put ?? []).map((file) => file.path).sort(),
      });
      const answer = queue.shift() ?? { exit_code: 0, output: "" };
      res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify(answer));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return { url: `http://127.0.0.1:${port}`, seen, ended: () => ended, close: () => new Promise((resolve) => server.close(() => resolve())) };
}

async function stepWorkspace(root: string): Promise<AgentWorkspace> {
  const org = join(root, "org");
  const ws: AgentWorkspace = {
    orgRoot: org,
    skillsRoot: join(org, "skills"),
    memoryRoot: join(org, "memory"),
    knowledgeRoot: join(org, "knowledge"),
    uploadsRoot: join(org, "uploads"),
    runsRoot: join(org, "runs"),
    threadUploadsRoot: join(org, "uploads", "t1"),
    runRoot: join(org, "runs", "r1"),
    artifactRoot: join(org, "runs", "r1", "artifacts"),
    binRoot: join(org, "runs", "r1", "bin"),
  };
  await mkdir(join(ws.skillsRoot, "xlsx"), { recursive: true });
  await writeFile(join(ws.skillsRoot, "xlsx", "run.py"), "print('rows')\n");
  await mkdir(join(ws.skillsRoot, "pdf"), { recursive: true });
  await writeFile(join(ws.skillsRoot, "pdf", "SKILL.md"), "# pdf\n");
  await mkdir(ws.artifactRoot, { recursive: true });
  await mkdir(join(ws.runRoot, ".openneko"), { recursive: true });
  await writeFile(join(ws.runRoot, ".openneko", "job.json"), "{}");
  return ws;
}

async function listFiles(dir: string, prefix = ""): Promise<string[]> {
  const out: string[] = [];
  for (const entry of (await readdir(dir, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) out.push(...await listFiles(join(dir, entry.name), rel));
    else out.push(`${rel}: ${readFileSync(join(dir, entry.name), "utf8")}`);
  }
  return out;
}

async function runStepScenario(s: StepScenario) {
  const root = await mkdtemp(join(tmpdir(), "wf-steps-"));
  const box = await fakeSandbox(s.answers);
  const before = process.env.OPENNEKO_SANDBOX_URL;
  if (s.sandbox) process.env.OPENNEKO_SANDBOX_URL = box.url;
  else delete process.env.OPENNEKO_SANDBOX_URL;
  try {
    const ws = await stepWorkspace(root);
    const events: AgentEvent[] = [];
    const results = await runScriptSteps({
      steps: s.steps as WorkflowStep[],
      workspace: ws,
      input: s.input,
      allowedSkills: s.allowed,
      emit: async (event) => { events.push(event); },
    });
    const norm = (text: string) => text.split(root).join("$ROOT");
    return JSON.parse(norm(JSON.stringify({
      results: results.map((r) => ({ ...r, durationMs: 0 })),
      events: events.map((e) => (e.type === "status" ? { ...e, message: e.message.replace(/ in \d+s$/, " in 0s") } : e)),
      requests: box.seen,
      ended: box.ended(),
      files: await listFiles(join(root, "org", "runs")),
    }))) as unknown;
  } finally {
    if (before === undefined) delete process.env.OPENNEKO_SANDBOX_URL;
    else process.env.OPENNEKO_SANDBOX_URL = before;
    await box.close();
    await rm(root, { recursive: true, force: true });
  }
}

const sha256 = (text: string) => createHash("sha256").update(text, "utf8").digest("hex");

describe("workflow turn fixtures for the Go turn", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("matches the fixture the Go test reads", async () => {
    const prompts = PROMPTS.map((c) => {
      const text = withFiles(c.input.filesEnabled, () => buildWorkflowRunnerPrompt({
        workflow: workflowOf(c.input),
        mode: c.input.mode,
        includeUxMetadata: c.input.includeUxMetadata,
        memoryContext: c.input.memoryContext,
        mcpTools: true,
        backend: c.input.backend as "ax",
        workspace: c.input.workspace,
        knowledge: c.input.knowledge,
        scriptData: c.input.scriptData,
      }));
      return { ...c, bytes: Buffer.byteLength(text, "utf8"), sha256: sha256(text) };
    });

    const now = "2026-11-13T08:30:00.123Z";
    vi.useFakeTimers({ now: new Date(now) });
    const seeds = SEEDS.map((c) => ({
      ...c,
      now,
      text: buildWorkflowSeedMessage({
        workflow: { name: c.name },
        triggerKind: c.triggerKind,
        userMessage: c.userMessage,
        triggerPayload: (c.payload ?? undefined) as Record<string, unknown> | undefined,
      }),
    }));
    vi.useRealTimers();

    const expands = EXPANDS.map((c) => {
      try {
        return { ...c, vars: VARS, result: expandArg(c.arg, VARS, c.input) };
      } catch (error) {
        return { ...c, vars: VARS, error: (error as Error).message };
      }
    });
    const stepResults = RESULTS.map((results) => ({ results, text: formatScriptStepResults(results) }));

    const servers = [];
    for (const files of [false, true]) {
      const run = await runCore([done("ok")], 0, files);
      servers.push({ filesEnabled: files, servers: run.servers[0] });
    }
    const continuations = [];
    for (const c of CONTINUATIONS) {
      const run = await runCore(c.turns, c.maxContinuations);
      continuations.push({ ...c, prompts: run.prompts, events: run.events, result: run.result });
    }
    const steps = [];
    for (const s of STEP_SCENARIOS) steps.push({ ...s, ...(await runStepScenario(s) as object) });

    const json = `${JSON.stringify({ prompts, seeds, expands, stepResults, servers, continuations, steps }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, json);
    expect(existsSync(FIXTURE)).toBe(true);
    expect(readFileSync(FIXTURE, "utf8")).toBe(json);
  });
});
