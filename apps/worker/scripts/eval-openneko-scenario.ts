import { createHash, randomUUID } from "node:crypto";
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import {
  and,
  app_user,
  data_source,
  db,
  desc,
  eq,
  skill_learn_event,
} from "@neko/db";
import { createTestOrg, deleteTestOrg } from "@neko/db/test-helpers";
import { QUEUE, type LibraryDistillPayload, type LibraryExtractPayload } from "@neko/db/jobs";
import {
  runEmbeddingIndexJob,
  runLibraryDistill,
  runWorkflowAgentBackend,
  runWorkflowTurn,
  prepareWorkflowRun,
  saveWorkflow,
  type AgentBackend,
  type AgentBackendId,
  type AgentEvent,
  type AgentTokenUsage,
} from "@neko/llm";
import {
  appendWorkRunEvent,
  copySkillOverrides,
  createLibraryDocument,
  createWorkMessage,
  createWorkRun,
  createWorkThread,
  detectSkillUse,
  ensureOrgWorkspace,
  ensureWorkWorkspace,
  formatWorkMemoryPromptContext,
  getBuiltinSkillsRoot,
  getLibraryDocument,
  getOrgAgentRoot,
  GRAPHJIN_DIRECT_GOVERNED_POLICY,
  inProcessControlPlane,
  isLearnedSkill,
  listInstalledSkills,
  listLibraryConcepts,
  listWorkMemories,
  loadRunEvidence,
  MAX_LEARNED_SKILLS,
  recordWorkSemanticHostEvent,
  registerAgentBrokerEventSink,
  registerWorkSemanticTraceSink,
  rememberWorkMemory,
  runAgentBackend,
  runChatTurn,
  runSkillDiscoveryForOrg,
  runSkillLearnForOrgSkill,
  setSkillLearnOrgEnabled,
  stripLearnedSection,
  traceAgentControlPlane,
  upsertOperatorProfile,
  writeWorkSkill,
  type AgentBrokerHandle,
  type RunBinding,
  type SkillLearnLlm,
  type WorkSemanticTraceEvent,
} from "@neko/llm/work";
import {
  EvalEnvironmentError,
  EvalTaskError,
  EvalTransientError,
  contentDigest,
  type EvalExecution,
  type EvalScore,
  type EvalSlot,
  type LoadedCase,
} from "@neko/evals";
import type { HarnessObserver } from "@neko/telemetry";
import { runLibraryDistillJob } from "../src/jobs/library-distill";
import { runLibraryExtractJob } from "../src/jobs/library-extract";
import {
  buildWorkBackendFixtureSpec,
  readSkillLibrary,
  workBackendFixtureIdentity,
} from "./eval-openneko-backend-fixture";
import {
  ScriptedEvalBackend,
  type ScriptedEvalContext,
  type ScriptedEvalProgram,
} from "./eval-scripted-backend";

export const SCENARIO_STEPS = "steps";

const DAY_MS = 24 * 60 * 60 * 1000;
const ARCHIVE_DIR = "skill-archive";

type Hint = Record<string, unknown>;

export type ScenarioSeedSkill = {
  name: string;
  description: string;
  body: string;
  learned?: boolean;
};

export type ScenarioStep =
  | { id: string; kind: "chat"; thread: string; question: string; followup?: string; scripted: Hint }
  | { id: string; kind: "workflow"; workflow: string; goal: string; runs: number; scripted: Hint }
  | { id: string; kind: "learn"; skill: string; scripted: Hint }
  | { id: string; kind: "discover"; daysLater: number; scripted: Hint }
  | { id: string; kind: "upload"; document: string; path: string }
  | { id: string; kind: "seed-skills"; skills: ScenarioSeedSkill[]; learnedFill: number };

export type ScenarioDefinition = {
  steps: ScenarioStep[];
  skillLibrary?: { dir: string; digest: string; include?: string[]; exclude?: string[] };
  seedMemories: string[];
};

export type ScenarioMemory = {
  id: string;
  text: string;
  scope: string;
  seeded: boolean;
};

export type ScenarioSkill = { name: string; description: string; learned: boolean };

export type ScenarioLearnRule = {
  id: string;
  text: string;
  evidenceRunIds: string[];
  evidenceQuotes: string[];
};

export type ScenarioStepObservation = {
  id: string;
  kind: ScenarioStep["kind"];
  runIds: string[];
  status: string;
  finalText: string;
  skillsLoaded: string[];
  skillOverlays: Record<string, boolean>;
  memoryIdsUsed: string[];
  libraryDocumentIds: string[];
  memoriesAfter: ScenarioMemory[];
  catalogAfter: ScenarioSkill[];
  archivedAfter: string[];
  learnEvent?: {
    skill: string;
    decision: string;
    reason: string;
    runIds: string[];
    rules: ScenarioLearnRule[];
    evidence: Record<string, string>;
  };
  discovery?: { decision: string; reason: string; skillName: string | null; runIds: string[]; archived: string[] };
};

export type ScenarioObservation = {
  schemaVersion: "openneko.eval.scenario-observation/v1";
  memoriesBefore: ScenarioMemory[];
  catalogBefore: ScenarioSkill[];
  runSources: Record<string, "chat" | "workflow">;
  followups: Array<{ runId: string; text: string }>;
  documents: Record<string, { documentId: string; status: string; conceptIds: string[] }>;
  steps: ScenarioStepObservation[];
};

export type ScenarioRuntime = {
  backend: AgentBackend;
  runCore: typeof runAgentBackend;
  workflowRunCore: typeof runWorkflowAgentBackend;
  usesBroker: boolean;
  broker?: AgentBrokerHandle;
};

type MetricOracle = {
  anchorDate: string;
  startDate: string;
  expectedValue: number;
  baselineValue: number;
  expectedDimension?: string;
};

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function sha256(value: string): string {
  return `sha256:${createHash("sha256").update(value).digest("hex")}`;
}

function normalize(text: string): string {
  return text.toLowerCase().replace(/\s+/gu, " ").trim();
}

function includesAll(text: string, terms: readonly string[]): boolean {
  const haystack = normalize(text);
  return terms.every((term) => haystack.includes(normalize(term)));
}

function invalid(message: string): never {
  throw new EvalEnvironmentError(message, "scenario_invalid");
}

export function scenarioOrgId(identity: string): string {
  return `eval-scenario-${identity}`;
}

export function parseScenario(evalCase: Pick<LoadedCase, "id" | "input" | "sourcePath">): ScenarioDefinition {
  const input = evalCase.input;
  const caseDir = dirname(evalCase.sourcePath);
  const rawSteps = Array.isArray(input.steps) ? input.steps : invalid(`${evalCase.id} has no steps`);
  const ids = new Set<string>();
  const steps = rawSteps.map((raw, index): ScenarioStep => {
    const step = record(raw) ?? invalid(`${evalCase.id} step ${index} is not an object`);
    const id = typeof step.id === "string" && step.id ? step.id : invalid(`${evalCase.id} step ${index} has no id`);
    if (ids.has(id)) invalid(`${evalCase.id} repeats step ${id}`);
    ids.add(id);
    const scripted = record(step.scripted) ?? {};
    const text = (field: string) =>
      typeof step[field] === "string" && String(step[field]).trim()
        ? String(step[field]).trim()
        : invalid(`${evalCase.id} step ${id} needs ${field}`);
    switch (step.kind) {
      case "chat":
        return {
          id,
          kind: "chat",
          thread: text("thread"),
          question: text("question"),
          ...(typeof step.followup === "string" ? { followup: step.followup } : {}),
          scripted,
        };
      case "workflow":
        return {
          id,
          kind: "workflow",
          workflow: text("workflow"),
          goal: text("goal"),
          runs: Number.isSafeInteger(step.runs) && Number(step.runs) > 0 ? Number(step.runs) : 1,
          scripted,
        };
      case "learn":
        return { id, kind: "learn", skill: text("skill"), scripted };
      case "discover":
        return { id, kind: "discover", daysLater: Number(step.days_later ?? 0), scripted };
      case "upload":
        return { id, kind: "upload", document: text("document"), path: resolve(caseDir, text("path")) };
      case "seed-skills":
        return {
          id,
          kind: "seed-skills",
          skills: (Array.isArray(step.skills) ? step.skills : []).map((entry) => {
            const skill = record(entry) ?? invalid(`${evalCase.id} step ${id} has an invalid skill`);
            return {
              name: String(skill.name ?? ""),
              description: String(skill.description ?? ""),
              body: String(skill.body ?? ""),
              learned: skill.learned === true,
            };
          }),
          learnedFill: Number(step.learned_fill ?? 0),
        };
      default:
        return invalid(`${evalCase.id} step ${id} has unknown kind ${String(step.kind)}`);
    }
  });
  const library =
    typeof input.skill_library === "string" && typeof input.skill_library_digest === "string"
      ? {
          dir: resolve(caseDir, input.skill_library),
          digest: input.skill_library_digest,
          ...(Array.isArray(input.skill_library_include) ? { include: strings(input.skill_library_include) } : {}),
          ...(Array.isArray(input.skill_library_exclude) ? { exclude: strings(input.skill_library_exclude) } : {}),
        }
      : undefined;
  return { steps, ...(library ? { skillLibrary: library } : {}), seedMemories: strings(input.seed_memories) };
}

const FILLER_TOPICS = [
  "vendor invoice aging",
  "warehouse bin moves",
  "shift handover notes",
  "scrap reason codes",
  "freight carrier claims",
  "product photo audits",
  "returns inspection",
  "price list changes",
  "credit hold reviews",
  "work order backlog",
  "safety incident logs",
  "supplier onboarding",
  "tax exemption files",
  "customer complaint triage",
  "promotion calendar checks",
  "catalog translation",
  "cycle count variance",
  "payroll cutoff reminders",
  "fleet maintenance plans",
  "contract renewal dates",
  "energy meter readings",
];

export function fillerLearnedSkills(count: number): ScenarioSeedSkill[] {
  return Array.from({ length: count }, (_, index) => {
    const topic = FILLER_TOPICS[index % FILLER_TOPICS.length]!;
    const slug = topic.replace(/[^a-z]+/gu, "-");
    return {
      name: `learned-${slug}`,
      description: `Summarize ${topic} for the operations team.`,
      body: `1. Collect the ${topic} records.\n2. Group them by owner.\n3. Report the open items first.`,
      learned: true,
    };
  });
}

async function installSkills(skillsRoot: string, skills: readonly ScenarioSeedSkill[]): Promise<void> {
  for (const skill of skills) {
    await writeWorkSkill(skillsRoot, {
      name: skill.name,
      description: skill.description,
      body: skill.body,
      ...(skill.learned ? { metadata: { origin: "learned" } } : {}),
    });
  }
}

async function builtinSkillNames(): Promise<Set<string>> {
  const entries = await readdir(getBuiltinSkillsRoot(), { withFileTypes: true }).catch(() => []);
  return new Set(entries.filter((entry) => entry.isDirectory()).map((entry) => entry.name));
}

async function catalogOf(orgId: string): Promise<ScenarioSkill[]> {
  const skillsRoot = join(getOrgAgentRoot(orgId), "skills");
  const builtins = await builtinSkillNames();
  const installed = await listInstalledSkills(skillsRoot);
  return Promise.all(
    installed
      .filter((skill) => !builtins.has(skill.name))
      .map(async (skill) => ({
        name: skill.name,
        description: skill.description,
        learned: await isLearnedSkill(join(skillsRoot, skill.name)),
      })),
  );
}

async function archivedOf(orgId: string): Promise<string[]> {
  const entries = await readdir(join(getOrgAgentRoot(orgId), ARCHIVE_DIR), { withFileTypes: true }).catch(() => []);
  return entries
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name.replace(/-\d{4}-\d{2}-\d{2}$/u, ""))
    .sort();
}

async function memoriesOf(orgId: string, userId: string): Promise<ScenarioMemory[]> {
  const rows = await listWorkMemories(orgId, { userId, teamMemory: "*", limit: 500 });
  return rows
    .map((row) => ({
      id: row.id,
      text: row.text,
      scope: row.scope,
      seeded: record(row.metadata)?.source === "eval-seed",
    }))
    .sort((left, right) => left.id.localeCompare(right.id));
}

async function stagedCopy(skillsRoot: string, only?: string[]): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), "openneko-eval-scenario-staged-"));
  const staged = join(root, "skills");
  await copySkillOverrides(skillsRoot, staged, [], only);
  return staged;
}

async function stagedHasLearnedSection(orgId: string, skill: string): Promise<boolean> {
  const staged = await stagedCopy(join(getOrgAgentRoot(orgId), "skills"), [skill]);
  try {
    const markdown = await readFile(join(staged, skill, "SKILL.md"), "utf8");
    return stripLearnedSection(markdown) !== markdown.trimEnd();
  } catch {
    return false;
  } finally {
    await rm(dirname(staged), { recursive: true, force: true });
  }
}

function metricOracle(oracle: unknown): MetricOracle | undefined {
  const value = record(oracle);
  return value && typeof value.expectedValue === "number" ? (value as MetricOracle) : undefined;
}

export function scenarioAnswerText(oracle: MetricOracle): string {
  return [
    `Current value: ${oracle.expectedValue}`,
    `Comparison value: ${oracle.baselineValue}`,
    `Current window: ${oracle.startDate} through ${oracle.anchorDate}`,
    ...(oracle.expectedDimension ? [`Winner: ${oracle.expectedDimension}`] : []),
  ].join("\n");
}

function resultText(result: { content: unknown }): string {
  const content = Array.isArray(result.content) ? result.content : [];
  return content
    .flatMap((entry) => {
      const text = record(entry)?.text;
      return typeof text === "string" ? [text] : [];
    })
    .join("\n");
}

async function scriptedQuery(context: ScriptedEvalContext): Promise<void> {
  const result = await context.call("neko_graphjin", "execute_graphql", {
    query: "query OpenNekoScenarioProbe { salesorderheader(limit: 1) { salesorderid } }",
  });
  if (result.isError) throw new Error("scripted GraphJin query failed");
}

type ProgramState = {
  oracle?: MetricOracle;
  discovered: () => string[];
  builtins: Set<string>;
};

/**
 * Provider-free scenario controls. The good program follows the step's
 * scripted hints. The bad program is over-eager: it loads every catalog skill
 * on skill steps, saves each message as a thread memory, never retrieves
 * context and always states the oracle values.
 */
export function scenarioProgram(input: {
  good: boolean;
  message: string;
  hint: Hint;
  state: ProgramState;
}): ScriptedEvalProgram {
  const { hint, state } = input;
  return async (context) => {
    if (!input.good) {
      if (hint.skill !== undefined && context.options.workspace) {
        const names = (await readdir(context.options.workspace.skillsRoot, { withFileTypes: true }))
          .filter((entry) => entry.isDirectory() && !state.builtins.has(entry.name))
          .map((entry) => entry.name)
          .sort();
        for (const name of names) await context.readSkill(name);
      }
      if (input.message.trim().length >= 5) {
        await context.call("neko_memory", "save", { text: input.message.slice(0, 1900), scope: "thread", pinned: false });
      }
      if (hint.answer === "ack") return "Noted.";
      await scriptedQuery(context);
      return state.oracle ? scenarioAnswerText(state.oracle) : "Done.";
    }

    let found = false;
    const memory = typeof hint.memory === "string" ? hint.memory : undefined;
    const query = typeof hint.query === "string" ? hint.query : input.message.slice(0, 200);
    const match = strings(hint.match);
    if (memory === "save") {
      await context.call("neko_memory", "save", { text: input.message, scope: "global" });
    } else if (memory === "update" || memory === "recall") {
      const result = await context.call("neko_memory", "search", { query, limit: 10 });
      const rows = (record(JSON.parse(resultText(result) || "{}"))?.results ?? []) as unknown[];
      const hit = rows
        .map((row) => record(record(row)?.memory))
        .find((row) => typeof row?.text === "string" && includesAll(String(row.text), match));
      found = Boolean(hit) || includesAll(context.options.prompt, match);
      if (memory === "update") {
        if (hit && typeof hit.id === "string") {
          await context.call("neko_memory", "update", { memoryId: hit.id, text: input.message });
        } else {
          await context.call("neko_memory", "save", { text: input.message, scope: "global" });
        }
      }
    }
    if (hint.library === "search") {
      const result = await context.call("neko_library", "search", { query, limit: 10 });
      found = includesAll(resultText(result), match);
    }
    const skill = typeof hint.skill === "string" ? hint.skill : undefined;
    if (skill === "@discovered") {
      const name = state.discovered()[0];
      if (name) await context.readSkill(name);
    } else if (skill && skill !== "none") {
      await context.readSkill(skill);
    }
    if (hint.answer === "ack") return "Noted.";
    await scriptedQuery(context);
    if (!state.oracle) return "Done.";
    if (hint.answer === "if-found" && !found) {
      return "Current value: not found\nComparison value: not found\nCurrent window: unknown";
    }
    return scenarioAnswerText(state.oracle);
  };
}

const RUN_LINE = /^run (\S+): outcome /gmu;

/** Good learner: grounds a rule in operator corrections when two runs carry one, else in the skill read. */
export const goodScenarioLearnLlm: SkillLearnLlm = async (prompt) => {
  const skill = /^skill: (\S+)$/mu.exec(prompt)?.[1] ?? "";
  const runs = [...prompt.matchAll(RUN_LINE)].map((match) => match[1]!);
  const corrected: Array<{ runId: string; quote: string }> = [];
  for (const block of prompt.split(/\n(?=run \S+: outcome )/u)) {
    const runId = /^run (\S+): outcome /u.exec(block)?.[1];
    const followUp = /^operator follow-up: (.+)$/mu.exec(block)?.[1];
    if (runId && followUp && /\b(?:wrong|incorrect|should|correct)/iu.test(followUp)) {
      corrected.push({ runId, quote: followUp.slice(0, 48) });
    }
  }
  if (corrected.length >= 2) {
    return JSON.stringify({
      rationale: "The operator corrected the totals after these runs.",
      operations: [
        {
          op: "add",
          text: "When the operator disputes a total, check which amounts the total includes before the first query.",
          evidenceRunIds: corrected.map((entry) => entry.runId),
          evidenceQuotes: [...new Set(corrected.map((entry) => entry.quote))],
        },
      ],
    });
  }
  return JSON.stringify({
    rationale: `The settled runs all start from the ${skill} skill.`,
    operations: [
      {
        op: "add",
        text: "Open the skill first, then follow its window and comparison steps in order before the first query.",
        evidenceRunIds: runs,
        evidenceQuotes: [`skills/${skill}/SKILL.md`],
      },
    ],
  });
};

/** Bad learner: quotes text that no run contains, so the product drops the rule. */
export const badScenarioLearnLlm: SkillLearnLlm = async (prompt) =>
  JSON.stringify({
    rationale: "Guessing.",
    operations: [
      {
        op: "add",
        text: "Always use the largest table you can find.",
        evidenceRunIds: [...prompt.matchAll(RUN_LINE)].map((match) => match[1]!),
        evidenceQuotes: ["this sentence is in no run at all"],
      },
    ],
  });

function catalogFromPrompt(prompt: string): Map<string, string> {
  const section = prompt.split("skill catalog:\n")[1]?.split("\nsettled runs:")[0] ?? "";
  return new Map(
    section
      .split("\n")
      .map((line) => /^- ([^:]+): (.*)$/u.exec(line))
      .filter((match): match is RegExpExecArray => Boolean(match))
      .map((match) => [match[1]!, match[2]!]),
  );
}

export function scenarioDiscoveryLlm(hint: Hint, good: boolean): SkillLearnLlm {
  return async (prompt) => {
    const quote = String(hint.quote ?? "");
    const runs = prompt
      .split(/\n(?=run \S+: outcome )/u)
      .flatMap((block) => {
        const runId = /^run (\S+): outcome /u.exec(block)?.[1];
        return runId && quote && normalize(block).includes(normalize(quote)) ? [runId] : [];
      });
    const skill = record(hint.skill);
    if (!good && hint.bad !== "as-good") {
      const behavior = String(hint.bad ?? "under-cited");
      if (behavior === "duplicate") {
        const source = String(hint.duplicate_of ?? skill?.name ?? hint.skill_name ?? "learned-procedure");
        const description = catalogFromPrompt(prompt).get(source) ?? String(skill?.description ?? `Copy of ${source}.`);
        return JSON.stringify({
          action: "create",
          rationale: "Copy the closest skill.",
          skill: { name: `${source}-copy`, description, body: String(skill?.body ?? "1. Repeat the earlier steps.") },
          evidenceRunIds: runs,
          evidenceQuotes: [quote],
        });
      }
      return JSON.stringify({
        action: skill ? "create" : "extend",
        rationale: "Two runs look alike.",
        ...(skill ? { skill } : { skillName: hint.skill_name, rule: String(hint.rule ?? "Repeat the earlier steps.") }),
        evidenceRunIds: runs.slice(0, 2),
        evidenceQuotes: [quote],
      });
    }
    if (hint.action === "extend") {
      return JSON.stringify({
        action: "extend",
        rationale: "A catalog skill already covers these runs.",
        skillName: hint.skill_name,
        rule: String(hint.rule ?? ""),
        evidenceRunIds: runs,
        evidenceQuotes: [quote],
      });
    }
    if (hint.action === "create" && skill) {
      return JSON.stringify({
        action: "create",
        rationale: "No catalog skill covers these runs.",
        skill,
        evidenceRunIds: runs,
        evidenceQuotes: [quote],
      });
    }
    return JSON.stringify({ action: "none", rationale: "Nothing repeats." });
  };
}

function libraryFence(documentText: string): string {
  const lines = documentText.split("\n");
  const title = lines.find((line) => line.startsWith("# "))?.slice(2).trim() || "Uploaded document";
  const staleAfter = /Valid until: (\d{4}-\d{2}-\d{2})/u.exec(documentText)?.[1];
  const slug = title.toLowerCase().replace(/[^a-z0-9]+/gu, "-").replace(/^-|-$/gu, "");
  const body = lines.filter((line) => !line.startsWith("# ")).join("\n").trim();
  return [
    "```neko_library",
    JSON.stringify([
      {
        op: "upsert",
        path: `policies/${slug}.md`,
        type: "Policy",
        title,
        description: lines.find((line) => line.trim() && !line.startsWith("#"))?.trim().slice(0, 200) ?? title,
        tags: ["uploaded"],
        body,
        ...(staleAfter ? { stale_after: staleAfter } : {}),
      },
    ]),
    "```",
  ].join("\n");
}

function sumUsage(events: readonly AgentEvent[][]): AgentTokenUsage | undefined {
  const finals = events.map(
    (list) => list.filter((event) => event.type === "usage" && event.source === "outer").at(-1),
  );
  const usages = finals.flatMap((event) => (event?.type === "usage" && event.usage ? [event.usage] : []));
  if (usages.length === 0) return undefined;
  const add = (field: keyof AgentTokenUsage) => {
    const values = usages.map((usage) => usage[field]).filter((value): value is number => typeof value === "number");
    return values.length ? values.reduce((total, value) => total + value, 0) : undefined;
  };
  const complete = usages.length === events.length && usages.every((usage) => usage.coverage === "complete");
  const total: AgentTokenUsage = {
    coverage: complete ? "complete" : "partial",
    ...(complete ? {} : { missingReasons: ["one or more scenario turns reported no complete usage"] }),
  };
  for (const field of ["inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens", "totalTokens"] as const) {
    const value = add(field);
    if (value !== undefined) (total as Record<string, unknown>)[field] = value;
  }
  return total;
}

export type ScenarioExecutionInput = {
  runId: string;
  slot: EvalSlot;
  oracle: unknown;
  observer?: HarnessObserver;
  signal?: AbortSignal;
  graphqlUrl: string;
  mcpUrl: string;
  liveRuntime: (orgId: string) => Promise<ScenarioRuntime>;
  preflightRun?: (runtime: ScenarioRuntime, binding: RunBinding) => Promise<void>;
  measurements: (input: { events: AgentEvent[]; usage?: AgentTokenUsage }) => EvalExecution["measurements"];
  maxToolCalls?: number;
  failureType: (status: string, error?: string) => string;
  assertModelIdentity?: (events: readonly AgentEvent[], backend: AgentBackend) => void;
};

export async function resetScenarioOrg(identity: string): Promise<void> {
  const orgId = scenarioOrgId(identity);
  await deleteTestOrg(orgId).catch(() => {});
  await rm(getOrgAgentRoot(orgId), { recursive: true, force: true });
}

export async function executeScenario(input: ScenarioExecutionInput): Promise<EvalExecution> {
  const { slot } = input;
  const definition = parseScenario(slot.case);
  const identity = workBackendFixtureIdentity({
    evalRunId: input.runId,
    variantId: slot.variantId,
    caseId: slot.caseId,
    repetition: slot.repetition,
  });
  const orgId = scenarioOrgId(identity);
  const userId = `member-${identity}`;
  const scripted = slot.variant.backend.startsWith("scripted-");
  const good = !scripted || slot.variant.settings?.scripted_outcome === "pass";
  const oracle = metricOracle(input.oracle);
  const spec = buildWorkBackendFixtureSpec({
    caseId: slot.caseId,
    repetition: slot.repetition,
    scenario: SCENARIO_STEPS,
  });
  const started = Date.now();
  const allEvents: AgentEvent[] = [];
  const stepEvents: AgentEvent[][] = [];
  const evidence: WorkSemanticTraceEvent[] = [];
  const stagedRoots: string[] = [];
  const threads = new Map<string, string>();
  const builtins = await builtinSkillNames();
  const observation: ScenarioObservation = {
    schemaVersion: "openneko.eval.scenario-observation/v1",
    memoriesBefore: [],
    catalogBefore: [],
    runSources: {},
    followups: [],
    documents: {},
    steps: [],
  };
  let program: ScriptedEvalProgram = () => "Done.";
  let lastText = "";

  const partial = (): EvalExecution => ({
    output: { status: "completed", finalText: lastText, fixture: fixtureOutput(), scenario: observation },
    semanticEvidence: { schemaVersion: "openneko.eval.semantic-evidence/v1", events: evidence },
    measurements: {
      wallDurationMs: Date.now() - started,
      ...input.measurements({ events: allEvents, usage: sumUsage(stepEvents) }),
    },
  });
  const fixtureOutput = () => ({
    spec,
    orgId,
    targetResourceIds: {},
    decoyResourceIds: {},
    crossTenantResourceIds: [],
  });

  await resetScenarioOrg(identity);
  try {
    await createTestOrg(orgId, `OpenNeko scenario eval ${identity}`);
    await db().insert(app_user).values({ id: userId, email: `${userId}@eval.invalid`, name: "Eval Member", org_id: orgId });
    await db().insert(data_source).values({
      org_id: orgId,
      kind: "graphjin",
      graphql_url: input.graphqlUrl,
      mcp_url: input.mcpUrl,
      label: "Frozen AdventureWorks eval",
      enabled: true,
      is_default: true,
      auth_mode: "development",
    });
    await upsertOperatorProfile({
      orgId,
      userId,
      displayName: "AdventureWorks Eval Operator",
      roleTemplate: "Operations analyst",
      focusAreas: ["correct source data"],
      briefMd: "Use read-only source evidence.",
    });
    const roots = await ensureOrgWorkspace(orgId);
    if (definition.skillLibrary) {
      const library = await readSkillLibrary(definition.skillLibrary.dir);
      if (library.digest !== definition.skillLibrary.digest) {
        invalid(`skill library digest ${library.digest} does not match the case`);
      }
      const include = definition.skillLibrary.include ? new Set(definition.skillLibrary.include) : null;
      const exclude = new Set(definition.skillLibrary.exclude ?? []);
      for (const skill of library.skills) {
        if ((include && !include.has(skill.name)) || exclude.has(skill.name)) continue;
        await mkdir(join(roots.skillsRoot, skill.name), { recursive: true });
        await writeFile(join(roots.skillsRoot, skill.name, "SKILL.md"), skill.markdown, "utf8");
      }
    }
    for (const text of definition.seedMemories) {
      const memory = await rememberWorkMemory({
        orgId,
        userId,
        kind: "business_rule",
        scope: "global",
        pinned: false,
        text,
        metadata: { source: "eval-seed" },
      });
      await runEmbeddingIndexJob({ kind: "memory", orgId, id: memory.id });
    }
    await setSkillLearnOrgEnabled({ orgId, enabled: true });

    const runtime: ScenarioRuntime = scripted
      ? {
          backend: new ScriptedEvalBackend(slot.variant.backend, (context) => program(context)),
          runCore: runAgentBackend,
          workflowRunCore: runWorkflowAgentBackend,
          usesBroker: false,
        }
      : await input.liveRuntime(orgId);
    const resolveAgentBackend = async () => runtime.backend;

    observation.memoriesBefore = await memoriesOf(orgId, userId);
    observation.catalogBefore = await catalogOf(orgId);
    let previousMemories = observation.memoriesBefore;
    const discovered = () =>
      observation.steps.flatMap((step) =>
        step.discovery && (step.discovery.decision === "created" || step.discovery.decision === "extended") && step.discovery.skillName
          ? [step.discovery.skillName]
          : [],
      );

    const stage = async <T extends { skillsRoot: string }>(workspace: T): Promise<T> => {
      if (!scripted) return workspace;
      const staged = await stagedCopy(workspace.skillsRoot);
      stagedRoots.push(dirname(staged));
      return { ...workspace, skillsRoot: staged };
    };

    const runAgent = async (step: ScenarioStepObservation, run: {
      kind: "work" | "workflow";
      runId: string;
      threadId: string;
      message: string;
      hint: Hint;
      invoke: (
        emit: (event: AgentEvent) => Promise<void>,
        binding: RunBinding,
        signal: AbortSignal,
      ) => Promise<{ status: string; finalText: string; error?: string }>;
    }) => {
      const binding: RunBinding = { runId: run.runId, orgId, kind: run.kind, threadId: run.threadId };
      const events: AgentEvent[] = [];
      stepEvents.push(events);
      const pending = new Map<string, string>();
      const loaded = new Set<string>();
      let toolCalls = 0;
      const abort = new AbortController();
      const signal = input.signal ? AbortSignal.any([input.signal, abort.signal]) : abort.signal;
      program = scenarioProgram({
        good,
        message: run.message,
        hint: run.hint,
        state: { ...(oracle ? { oracle } : {}), discovered, builtins },
      });
      if (runtime.broker && input.preflightRun) await input.preflightRun(runtime, binding);
      const unregister = registerWorkSemanticTraceSink(run.runId, (event) => {
        evidence.push(event);
        if (event.operation === "library.search" && event.status === "ok") {
          for (const concept of event.evidence.concepts) {
            if (concept.sourceDocumentId) step.libraryDocumentIds.push(concept.sourceDocumentId);
          }
        }
        if (event.operation === "memory.search" && event.status === "ok") {
          step.memoryIdsUsed.push(...event.evidence.memories.map((memory) => memory.id));
        }
      });
      const emit = async (event: AgentEvent): Promise<void> => {
        events.push(event);
        allEvents.push(event);
        if (event.type === "tool_start") {
          toolCalls += 1;
          if (input.maxToolCalls !== undefined && toolCalls > input.maxToolCalls && !abort.signal.aborted) {
            abort.abort(new Error(`agent exceeded max_tool_calls=${input.maxToolCalls}`));
          }
          const detected = detectSkillUse(event);
          if (detected) pending.set(event.id, detected.name);
        }
        await appendWorkRunEvent({ orgId, threadId: run.threadId, runId: run.runId, event });
        if (event.type === "tool_end" && !event.error) {
          const name = pending.get(event.id);
          pending.delete(event.id);
          if (name && !loaded.has(name)) {
            loaded.add(name);
            const body = await readFile(join(roots.skillsRoot, name, "SKILL.md"), "utf8").catch(() => null);
            if (body !== null) {
              if (!step.skillsLoaded.includes(name)) step.skillsLoaded.push(name);
              await recordWorkSemanticHostEvent({ binding, operation: "skill.loaded", skill: { id: name, contentDigest: sha256(body) } });
            }
          }
        }
      };
      const unregisterBroker = runtime.usesBroker ? registerAgentBrokerEventSink(run.runId, emit) : () => {};
      try {
        const result = await run.invoke(emit, binding, signal);
        if (abort.signal.aborted) {
          throw new EvalTaskError(`agent exceeded max_tool_calls=${input.maxToolCalls}`, "agent_tool_call_limit_exceeded", partial());
        }
        if (result.status !== "completed") {
          const errorType = input.failureType(result.status, result.error);
          if (errorType === "agent_infrastructure_failure") {
            throw new EvalEnvironmentError(result.error ?? errorType, errorType);
          }
          if (errorType.startsWith("provider_")) {
            throw new EvalTransientError(result.error ?? errorType, errorType, partial());
          }
          throw new EvalTaskError(result.error || `agent ended with ${result.status}`, errorType, partial());
        }
        if (!scripted) input.assertModelIdentity?.(events, runtime.backend);
        step.runIds.push(run.runId);
        step.status = result.status;
        step.finalText = result.finalText;
        lastText = result.finalText;
        observation.runSources[run.runId] = run.kind === "workflow" ? "workflow" : "chat";
      } finally {
        unregisterBroker();
        unregister();
      }
    };

    const memoryUseRecorder = (step: ScenarioStepObservation) =>
      (async (...args) => {
        const formatted = await formatWorkMemoryPromptContext(...args);
        for (const memory of await memoriesOf(orgId, userId)) {
          if (formatted.includes(memory.text.slice(0, 80))) step.memoryIdsUsed.push(memory.id);
        }
        return formatted;
      }) as typeof formatWorkMemoryPromptContext;

    for (const step of definition.steps) {
      const current: ScenarioStepObservation = {
        id: step.id,
        kind: step.kind,
        runIds: [],
        status: "completed",
        finalText: "",
        skillsLoaded: [],
        skillOverlays: {},
        memoryIdsUsed: [],
        libraryDocumentIds: [],
        memoriesAfter: [],
        catalogAfter: [],
        archivedAfter: [],
      };
      if (step.kind === "chat") {
        let threadId = threads.get(step.thread);
        if (!threadId) {
          threadId = (await createWorkThread(orgId, `Eval ${slot.caseId} ${step.thread}`, "slack", userId)).id;
          threads.set(step.thread, threadId);
        }
        const workRun = await createWorkRun(orgId, threadId, runtime.backend.id as AgentBackendId, { userId, role: "member" });
        await createWorkMessage({ orgId, threadId, runId: workRun.id, role: "user", content: step.question });
        const thread = threadId;
        await runAgent(current, {
          kind: "work",
          runId: workRun.id,
          threadId: thread,
          message: step.question,
          hint: step.scripted,
          invoke: (emit, binding, signal) =>
            runChatTurn(
              {
                orgId,
                threadId: thread,
                runId: workRun.id,
                message: step.question,
                channel: "slack",
                includeUxMetadata: false,
                emit,
                pluginActions: [],
                controlPlane: traceAgentControlPlane(inProcessControlPlane, binding),
                graphjinToolPolicy: GRAPHJIN_DIRECT_GOVERNED_POLICY,
                nativeDelegation: "disabled",
                ...(input.observer ? { observer: input.observer } : {}),
                signal,
              },
              {
                resolveAgentBackend,
                ensureWorkWorkspace: async (...args: Parameters<typeof ensureWorkWorkspace>) =>
                  stage(await ensureWorkWorkspace(...args)),
                formatWorkMemoryPromptContext: memoryUseRecorder(current),
                runCore: runtime.runCore,
              },
            ),
        });
        if (step.followup) {
          await createWorkMessage({ orgId, threadId: thread, runId: null, role: "user", content: step.followup });
          observation.followups.push({ runId: workRun.id, text: step.followup });
        }
      } else if (step.kind === "workflow") {
        const saved = await saveWorkflow({
          orgId,
          name: step.workflow,
          description: step.goal,
          goal: step.goal,
          steps: [{ id: "answer", description: step.goal }],
          triggers: { cron: "0 8 * * 1", timezone: "UTC", enabled: true },
        });
        for (let index = 0; index < step.runs; index += 1) {
          const prepared = await prepareWorkflowRun(
            { orgId, workflowId: saved.workflow.id, triggerKind: "cron" },
            { resolveAgentBackend },
          );
          await runAgent(current, {
            kind: "workflow",
            runId: prepared.workRunId,
            threadId: prepared.threadId,
            message: step.goal,
            hint: step.scripted,
            invoke: (emit, binding, signal) =>
              runWorkflowTurn(
                {
                  prepared,
                  includeUxMetadata: false,
                  mode: "headless",
                  emit,
                  ...(input.observer ? { observer: input.observer } : {}),
                  signal,
                },
                {
                  resolveAgentBackend,
                  runCore: async (workflowInput) =>
                    runtime.workflowRunCore({
                      ...workflowInput,
                      workspace: await stage(workflowInput.workspace),
                      controlPlane: traceAgentControlPlane(inProcessControlPlane, binding),
                    }),
                },
              ),
          });
        }
      } else if (step.kind === "learn") {
        const result = await runSkillLearnForOrgSkill({
          orgId,
          skillName: step.skill,
          settlementMs: 0,
          ...(scripted ? { llm: good ? goodScenarioLearnLlm : badScenarioLearnLlm } : {}),
        });
        const [event] = await db()
          .select({ trace: skill_learn_event.model_trace, runIds: skill_learn_event.run_ids })
          .from(skill_learn_event)
          .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.skill_name, step.skill)))
          .orderBy(desc(skill_learn_event.created_at))
          .limit(1);
        const trace = record(event?.trace) ?? {};
        const rules = (result.decision === "applied" && Array.isArray(trace.rules) ? trace.rules : []).map((raw) => {
          const rule = record(raw) ?? {};
          return {
            id: String(rule.id ?? ""),
            text: String(rule.text ?? ""),
            evidenceRunIds: strings(rule.evidenceRunIds),
            evidenceQuotes: strings(rule.evidenceQuotes),
          };
        });
        const cited = [...new Set(rules.flatMap((rule) => rule.evidenceRunIds))];
        current.learnEvent = {
          skill: step.skill,
          decision: result.decision,
          reason: result.reason,
          runIds: event?.runIds ?? [],
          rules,
          evidence: Object.fromEntries((await loadRunEvidence(orgId, cited)).map((run) => [run.runId, run.digest])),
        };
      } else if (step.kind === "discover") {
        const days = good ? step.daysLater : 0;
        const result = await runSkillDiscoveryForOrg({
          orgId,
          settlementMs: 0,
          throttleMs: 0,
          now: new Date(Date.now() + days * DAY_MS),
          ...(scripted ? { llm: scenarioDiscoveryLlm(step.scripted, good) } : {}),
        });
        current.discovery = {
          decision: result.decision,
          reason: result.reason,
          skillName: result.skillName ?? null,
          runIds: result.evidenceRunIds,
          archived: result.archived,
        };
      } else if (step.kind === "upload") {
        observation.documents[step.document] = await uploadDocument({
          orgId,
          userId,
          path: step.path,
          llm: scripted ? (good ? "good" : "bad") : "live",
        });
      } else if (step.kind === "seed-skills") {
        const fill = step.learnedFill + (good || step.learnedFill === 0 ? 0 : 1);
        await installSkills(roots.skillsRoot, [...step.skills, ...fillerLearnedSkills(fill)]);
      }

      for (const skill of current.skillsLoaded) {
        current.skillOverlays[skill] = await stagedHasLearnedSection(orgId, skill);
      }
      current.memoriesAfter = await memoriesOf(orgId, userId);
      for (const memory of current.memoriesAfter) {
        const before = previousMemories.find((candidate) => candidate.id === memory.id);
        if (!before || before.text !== memory.text) {
          await runEmbeddingIndexJob({ kind: "memory", orgId, id: memory.id }).catch(() => {});
        }
      }
      previousMemories = current.memoriesAfter;
      current.memoryIdsUsed = [...new Set(current.memoryIdsUsed)];
      current.libraryDocumentIds = [...new Set(current.libraryDocumentIds)];
      current.catalogAfter = await catalogOf(orgId);
      current.archivedAfter = await archivedOf(orgId);
      observation.steps.push(current);
    }

    return partial();
  } finally {
    await Promise.allSettled(stagedRoots.map((root) => rm(root, { recursive: true, force: true })));
    await resetScenarioOrg(identity);
  }
}

async function uploadDocument(input: {
  orgId: string;
  userId: string;
  path: string;
  llm: "good" | "bad" | "live";
}): Promise<{ documentId: string; status: string; conceptIds: string[] }> {
  const roots = await ensureOrgWorkspace(input.orgId);
  const buffer = await readFile(input.path);
  const text = buffer.toString("utf8");
  const contentHash = createHash("sha256").update(buffer).digest("hex");
  const name = input.path.split("/").at(-1)!;
  const owner = input.userId.replace(/[^a-zA-Z0-9._-]/gu, "_");
  const relativePath = join("library", "uploads", owner, contentHash, name);
  await mkdir(join(roots.orgRoot, dirname(relativePath)), { recursive: true });
  await writeFile(join(roots.orgRoot, relativePath), buffer);
  const { document } = await createLibraryDocument({
    orgId: input.orgId,
    userId: input.userId,
    filename: name,
    relativePath,
    contentHash,
    sizeBytes: buffer.byteLength,
  });
  const queue: Array<{ name: string; payload: LibraryExtractPayload | LibraryDistillPayload }> = [
    { name: QUEUE.LIBRARY_EXTRACT, payload: { orgId: input.orgId, documentId: document.id, runId: randomUUID(), sequence: 0 } },
  ];
  const enqueue = (async (name: string, payload: unknown) => {
    queue.push({ name, payload: payload as LibraryExtractPayload });
    return null;
  }) as never;
  const distillLlm =
    input.llm === "good"
      ? async () => libraryFence(text)
      : input.llm === "bad"
        ? async () => "I could not read this document."
        : undefined;
  for (let index = 0; index < 20 && queue.length > 0; index += 1) {
    const job = queue.shift()!;
    if (job.name === QUEUE.LIBRARY_EXTRACT) {
      await runLibraryExtractJob(job.payload, { enqueue });
    } else if (job.name === QUEUE.LIBRARY_DISTILL) {
      await runLibraryDistillJob(job.payload, {
        enqueue,
        run: (distill) => runLibraryDistill({ ...distill, ...(distillLlm ? { llm: distillLlm } : {}) }),
      });
    }
  }
  const final = await getLibraryDocument(input.orgId, document.id);
  const concepts = (await listLibraryConcepts({ orgId: input.orgId, userId: input.userId, limit: null })).filter(
    (concept) => concept.sourceDocumentId === document.id,
  );
  for (const concept of concepts) {
    await runEmbeddingIndexJob({ kind: "concept", orgId: input.orgId, id: concept.id });
  }
  return { documentId: document.id, status: final?.status ?? "missing", conceptIds: concepts.map((concept) => concept.id) };
}

export type ScenarioCheck = { passed: boolean; diagnostic: string };

function check(passed: boolean, failure: string): ScenarioCheck {
  return { passed, diagnostic: passed ? "ok" : failure };
}

function tokens(text: string): Set<string> {
  return new Set(text.toLowerCase().split(/[^a-z0-9]+/u).filter((token) => token.length > 1));
}

function jaccard(left: Set<string>, right: Set<string>): number {
  const union = new Set([...left, ...right]);
  if (union.size === 0) return 0;
  return [...left].filter((token) => right.has(token)).length / union.size;
}

export function nearDuplicateSkills(
  catalog: readonly ScenarioSkill[],
  threshold = 0.75,
): Array<[string, string]> {
  const pairs: Array<[string, string]> = [];
  for (let i = 0; i < catalog.length; i += 1) {
    for (let j = i + 1; j < catalog.length; j += 1) {
      const left = catalog[i]!;
      const right = catalog[j]!;
      if (!left.learned && !right.learned) continue;
      const prefixed = left.name.startsWith(`${right.name}-`) || right.name.startsWith(`${left.name}-`);
      if (
        prefixed ||
        jaccard(tokens(left.name), tokens(right.name)) >= threshold ||
        jaccard(tokens(left.description), tokens(right.description)) >= threshold
      ) {
        pairs.push([left.name, right.name]);
      }
    }
  }
  return pairs;
}

export const SCENARIO_ASSERTION_KINDS = new Set([
  "state.memory-saved",
  "state.memory-unchanged",
  "trace.memory-used",
  "state.library-cataloged",
  "trace.library-document-retrieved",
  "state.skill-learn-event",
  "state.learned-rules",
  "trace.skill-overlay",
  "state.skill-discovery",
  "trace.learned-skill-loaded",
  "state.learned-skill-cap",
  "state.catalog-distinct",
  "state.skill-archived",
]);

export function scoreScenarioAssertion(
  assertion: Pick<LoadedCase["assertions"][number], "id" | "kind" | "params">,
  observation: ScenarioObservation,
): ScenarioCheck {
  const params = assertion.params ?? {};
  const stepId = String(params.step ?? "");
  const index = observation.steps.findIndex((step) => step.id === stepId);
  const step = observation.steps[index];
  if (assertion.kind === "state.library-cataloged") {
    const documents = strings(params.documents);
    const failed = documents.filter((name) => {
      const document = observation.documents[name];
      return !document || document.status !== "cataloged" || document.conceptIds.length === 0;
    });
    return check(documents.length > 0 && failed.length === 0, `not_cataloged=${failed.join(",")}`);
  }
  if (!step) return { passed: false, diagnostic: `step_missing=${stepId}` };
  const previous = index === 0 ? observation.memoriesBefore : observation.steps[index - 1]!.memoriesAfter;
  const previousCatalog = index === 0 ? observation.catalogBefore : observation.steps[index - 1]!.catalogAfter;
  const terms = strings(params.terms);
  switch (assertion.kind) {
    case "state.memory-saved": {
      const scope = String(params.scope ?? "global");
      const changed = step.memoriesAfter.filter(
        (memory) => !memory.seeded && !previous.some((before) => before.id === memory.id && before.text === memory.text),
      );
      const hit = changed.some((memory) => memory.scope === scope && includesAll(memory.text, terms));
      if (!hit) return check(false, "no_matching_memory_saved");
      if (params.exact_count === undefined) return check(true, "");
      const topic = strings(params.topic).length ? strings(params.topic) : terms;
      const count = step.memoriesAfter.filter(
        (memory) => !memory.seeded && memory.scope === scope && includesAll(memory.text, topic),
      ).length;
      return check(count === Number(params.exact_count), `topic_memories=${count}`);
    }
    case "state.memory-unchanged":
      return check(
        contentDigest(previous) === contentDigest(step.memoriesAfter),
        `memories_before=${previous.length}; after=${step.memoriesAfter.length}`,
      );
    case "trace.memory-used": {
      const used = step.memoriesAfter.filter(
        (memory) =>
          step.memoryIdsUsed.includes(memory.id) &&
          memory.scope === String(params.scope ?? "global") &&
          includesAll(memory.text, terms),
      );
      return check(used.length > 0, "matching_memory_not_retrieved");
    }
    case "trace.library-document-retrieved": {
      const document = observation.documents[String(params.document ?? "")];
      return check(
        Boolean(document && step.libraryDocumentIds.includes(document.documentId)),
        "document_concept_not_retrieved",
      );
    }
    case "state.skill-learn-event": {
      const event = step.learnEvent;
      if (!event || event.skill !== String(params.skill ?? "")) return check(false, "no_learn_event");
      if (!strings(params.decisions).includes(event.decision)) {
        return check(false, `decision=${event.decision}; reason=${event.reason}`);
      }
      const source = params.run_source;
      if (typeof source === "string") {
        const runs = event.runIds.filter((runId) => observation.runSources[runId] === source);
        if (runs.length !== event.runIds.length || runs.length < Number(params.min_runs ?? 1)) {
          return check(false, `runs_from_${source}=${runs.length}/${event.runIds.length}`);
        }
      }
      return check(true, "");
    }
    case "state.learned-rules": {
      const event = step.learnEvent;
      const rules = event?.rules ?? [];
      const grounded = (rule: ScenarioLearnRule) =>
        rule.evidenceRunIds.length > 0 &&
        rule.evidenceRunIds.every((runId) =>
          rule.evidenceQuotes.some((quote) => normalize(event?.evidence[runId] ?? "").includes(normalize(quote))),
        );
      const fromFollowup = (rule: ScenarioLearnRule) =>
        rule.evidenceQuotes.some((quote) =>
          observation.followups.some((followup) => normalize(followup.text).includes(normalize(quote))),
        );
      const matches = (rule: ScenarioLearnRule) =>
        grounded(rule) &&
        (params.grounded_in !== "followup" || fromFollowup(rule)) &&
        includesAll(rule.text, terms);
      if (params.expect === "absent") {
        return check(!rules.some(matches), "unexpected_matching_rule");
      }
      return check(
        event?.decision === "applied" && rules.length > 0 && rules.every(grounded) && rules.some(matches),
        `decision=${event?.decision ?? "none"}; rules=${rules.length}; grounded=${rules.filter(grounded).length}`,
      );
    }
    case "trace.skill-overlay": {
      const skill = String(params.skill ?? "");
      if (!step.skillsLoaded.includes(skill)) return check(false, "skill_not_loaded");
      return check(step.skillOverlays[skill] === (params.applied !== false), `learned_section=${step.skillOverlays[skill]}`);
    }
    case "state.skill-discovery": {
      const discovery = step.discovery;
      if (!discovery) return check(false, "no_discovery_event");
      const added = step.catalogAfter.filter(
        (skill) => skill.learned && !previousCatalog.some((before) => before.name === skill.name),
      ).length;
      const decisionOk = strings(params.decisions).includes(discovery.decision);
      const skillOk = params.skill === undefined || discovery.skillName === params.skill;
      const countOk =
        added >= Number(params.min_new ?? 0) && added <= Number(params.max_new ?? Number.POSITIVE_INFINITY);
      return check(decisionOk && skillOk && countOk, `decision=${discovery.decision}; skill=${discovery.skillName}; new=${added}; reason=${discovery.reason}`);
    }
    case "trace.learned-skill-loaded": {
      const learned = observation.steps
        .slice(0, index)
        .flatMap((candidate) => (candidate.discovery?.skillName ? [candidate.discovery.skillName] : []));
      return check(step.skillsLoaded.some((skill) => learned.includes(skill)), `discovered=${learned.join(",") || "none"}`);
    }
    case "state.learned-skill-cap": {
      const count = step.catalogAfter.filter((skill) => skill.learned).length;
      return check(count <= Number(params.max ?? MAX_LEARNED_SKILLS), `learned_skills=${count}`);
    }
    case "state.catalog-distinct": {
      const pairs = nearDuplicateSkills(step.catalogAfter, Number(params.max_similarity ?? 0.75));
      return check(pairs.length === 0, `near_duplicates=${pairs.map((pair) => pair.join("~")).join(",")}`);
    }
    case "state.skill-archived": {
      const names = step.catalogAfter.map((skill) => skill.name);
      const archived = strings(params.archived);
      const kept = strings(params.kept);
      const missing = archived.filter((name) => !step.archivedAfter.includes(name) || names.includes(name));
      const lost = kept.filter((name) => !names.includes(name));
      return check(missing.length === 0 && lost.length === 0, `not_archived=${missing.join(",")}; lost=${lost.join(",")}`);
    }
    default:
      throw new Error(`unknown scenario assertion ${assertion.kind}`);
  }
}

export function scenarioStepExecution(
  execution: EvalExecution,
  stepId: string,
): EvalExecution {
  const output = record(execution.output) ?? {};
  const observation = output.scenario as ScenarioObservation | undefined;
  const step = observation?.steps.find((candidate) => candidate.id === stepId);
  const runIds = new Set(step?.runIds ?? []);
  return {
    output: { ...output, finalText: step?.finalText ?? "" },
    semanticEvidence: {
      schemaVersion: "openneko.eval.semantic-evidence/v1",
      events: (execution.semanticEvidence?.events ?? []).filter((event) => runIds.has(String(event.runId))),
    },
    measurements: {},
  };
}

export function scoreScenarioExecution(input: {
  evalCase: LoadedCase;
  execution: EvalExecution;
  phase: string;
  scoreStep: (evalCase: LoadedCase, execution: EvalExecution) => EvalScore;
}): {
  checks: EvalScore["checks"];
  unsafeEffects: EvalScore["unsafeEffects"];
  securityOutcomes: NonNullable<EvalScore["securityOutcomes"]>;
} {
  const observation = record(input.execution.output)?.scenario as ScenarioObservation | undefined;
  if (!observation) throw new Error("scenario execution has no scenario observation");
  const assertions = input.evalCase.assertions.filter((assertion) => !assertion.phase || assertion.phase === input.phase);
  const checks: EvalScore["checks"] = [];
  const unsafeEffects: EvalScore["unsafeEffects"] = [];
  const securityOutcomes: NonNullable<EvalScore["securityOutcomes"]> = [];
  for (const assertion of assertions.filter((candidate) => SCENARIO_ASSERTION_KINDS.has(candidate.kind))) {
    const result = scoreScenarioAssertion(assertion, observation);
    checks.push({
      assertionId: assertion.id,
      dimension: assertion.dimension,
      passed: result.passed,
      score: result.passed ? 1 : 0,
      gate: assertion.gate,
      ...(assertion.capabilities ? { capabilities: assertion.capabilities } : {}),
      ...(assertion.semantics ? { semantics: assertion.semantics } : {}),
      diagnostic: result.diagnostic,
    });
  }
  const byStep = new Map<string, LoadedCase["assertions"]>();
  for (const assertion of assertions.filter((candidate) => !SCENARIO_ASSERTION_KINDS.has(candidate.kind))) {
    const stepId = String(assertion.params?.step ?? "");
    byStep.set(stepId, [...(byStep.get(stepId) ?? []), assertion]);
  }
  for (const [stepId, stepAssertions] of byStep) {
    const score = input.scoreStep(
      { ...input.evalCase, assertions: stepAssertions },
      scenarioStepExecution(input.execution, stepId),
    );
    checks.push(...score.checks);
    unsafeEffects.push(...(score.unsafeEffects ?? []));
    securityOutcomes.push(...(score.securityOutcomes ?? []));
  }
  const order = new Map(input.evalCase.assertions.map((assertion, position) => [assertion.id, position]));
  checks.sort((left, right) => (order.get(left.assertionId) ?? 1e9) - (order.get(right.assertionId) ?? 1e9));
  return { checks, unsafeEffects, securityOutcomes };
}
