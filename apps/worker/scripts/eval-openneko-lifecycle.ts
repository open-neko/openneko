import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { app_user, builtinGroupId, data_source, db, pool, setLocalAdministrator } from "@neko/db";
import { createTestOrg, dbReachable, seedProvider } from "@neko/db/test-helpers";
import {
  EvalEnvironmentError,
  createScore,
  shortDigest,
  type EvalDriver,
  type EvalPlan,
  type LoadedEval,
} from "@neko/evals";
import {
  createActionPolicy,
  getActionRequest,
  listActionExecutions,
  listWatchers,
  registerActionAdapter,
  resetWatcher,
  saveWorkflow,
  setWatcherEnabled,
  upsertWatcher,
  verifyAuditChain,
  type WatcherRecord,
} from "@neko/llm/workflows";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import { ensureOrgWorkspace } from "@neko/llm/work";
import { EvalInstallHost, type EvalEpisode, type EvalInstallHostOptions } from "./eval-install";
import {
  RunEventFeed,
  ScriptedModelServer,
  type ScriptedModelContext,
  type ScriptedModelProgram,
  type ScriptedToolResult,
} from "./eval-scripted-model";

const ACTION_KIND = "openneko_eval_mutation";
const PRIORITY_KIND = "openneko_eval_priority";
const SWEEP_EPOCH = new Date("2030-01-01T00:00:00.000Z");
const SCORER = {
  id: "openneko.lifecycle",
  version: "1.0.0",
  definition: {
    assertions: "exact deterministic field checks against product-path state",
    sideEffects: "isolated organization plus in-memory action adapter",
  },
} as const;

function valueAtPath(value: unknown, path: string): unknown {
  return path
    .split(".")
    .filter(Boolean)
    .reduce<unknown>((current, segment) => {
      if (!current || typeof current !== "object") return undefined;
      return (current as Record<string, unknown>)[segment];
    }, value);
}

export class EvalGraphqlSource {
  url = "";
  readonly queries: string[] = [];
  private readonly values = new Map<string, unknown>();

  private constructor(private readonly server: Server) {}

  static async start(host = "127.0.0.2"): Promise<EvalGraphqlSource> {
    let self: EvalGraphqlSource | undefined;
    const server = createServer((req, res) => void self!.handle(req, res));
    self = new EvalGraphqlSource(server);
    await new Promise<void>((done, reject) => {
      server.once("error", reject);
      server.listen(0, host, () => done());
    });
    const address = server.address();
    if (!address || typeof address !== "object") throw new Error("the eval GraphQL source has no address");
    self.url = `http://${host}:${address.port}/api/v1/graphql`;
    return self;
  }

  set(root: string, value: unknown): void {
    this.values.set(root, value);
  }

  clear(): void {
    this.values.clear();
    this.queries.length = 0;
  }

  answer(query: string): Record<string, unknown> {
    const root = /\{\s*([A-Za-z_]\w*)/u.exec(query)?.[1] ?? "";
    if (!this.values.has(root)) return { errors: [{ message: `unknown field ${root || "(none)"}` }] };
    const value = this.values.get(root);
    if (value instanceof Error) return { errors: [{ message: value.message }] };
    return { data: { [root]: value } };
  }

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    let body = "";
    for await (const chunk of req) body += chunk;
    let query = "";
    try {
      query = String((JSON.parse(body) as { query?: unknown }).query ?? "");
    } catch {}
    this.queries.push(query);
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify(this.answer(query)));
  }

  async close(): Promise<void> {
    this.server.closeAllConnections();
    await new Promise<void>((done) => this.server.close(() => done()));
  }
}

export type ConditionWatcher = {
  name: string;
  root: string;
  valuePath: string;
  op: string;
  threshold: unknown;
};

export const CONDITION_WATCHERS: readonly ConditionWatcher[] = [
  { name: "m-path-array", root: "orders", valuePath: "orders.0.count", op: "eq", threshold: 7 },
  { name: "m-path-missing", root: "orders", valuePath: "orders.1.count", op: "gte", threshold: 0 },
  { name: "m-gt", root: "m_gt", valuePath: "m_gt", op: "gt", threshold: 3 },
  { name: "m-gte", root: "m_gte", valuePath: "m_gte", op: "gte", threshold: 3 },
  { name: "m-lt", root: "m_lt", valuePath: "m_lt", op: "lt", threshold: 3 },
  { name: "m-lte", root: "m_lte", valuePath: "m_lte", op: "lte", threshold: 3 },
  { name: "m-eq", root: "m_eq", valuePath: "m_eq", op: "eq", threshold: "P0" },
  { name: "m-ne", root: "m_ne", valuePath: "m_ne", op: "ne", threshold: "P0" },
  { name: "m-non-numeric", root: "m_text", valuePath: "m_text", op: "gt", threshold: 3 },
  { name: "m-changed", root: "m_changed", valuePath: "m_changed", op: "changed", threshold: null },
];

const CONDITION_VALUES: Record<string, unknown> = {
  orders: [{ count: 7 }],
  m_gt: 5,
  m_gte: 3,
  m_lt: 2,
  m_lte: 3,
  m_eq: "P0",
  m_ne: "P1",
  m_text: "not-a-number",
  m_changed: 5,
};

type SweepResult = { checked: number; fired: Array<{ watcherId: string; name: string; value: unknown }> };

export function parseSweep(result: Record<string, unknown>): SweepResult {
  const fired = Array.isArray(result.fired) ? result.fired : [];
  return {
    checked: Number(result.checked ?? 0),
    fired: fired.map((entry) => {
      const item = (entry ?? {}) as Record<string, unknown>;
      return { watcherId: String(item.watcherId ?? ""), name: String(item.name ?? ""), value: item.value };
    }),
  };
}

export function conditionMatrixFrom(input: {
  sweeps: [SweepResult, SweepResult, SweepResult];
  watchers: readonly Pick<WatcherRecord, "name" | "lastValue" | "lastError">[];
  changedAfterFirst: unknown;
}): Record<string, unknown> {
  const [first, second, third] = input.sweeps;
  const firedIn = (sweep: SweepResult, name: string) => sweep.fired.some((entry) => entry.name === name);
  const state = (name: string) => input.watchers.find((watcher) => watcher.name === name);
  const clean = (name: string) => (state(name)?.lastError ?? null) === null;
  return {
    pathObjectArray: firedIn(first, "m-path-array") && state("m-path-array")?.lastValue === 7,
    pathMissing:
      !firedIn(first, "m-path-missing") &&
      (state("m-path-missing")?.lastValue ?? null) === null &&
      clean("m-path-missing"),
    gt: firedIn(first, "m-gt"),
    gte: firedIn(first, "m-gte"),
    lt: firedIn(first, "m-lt"),
    lte: firedIn(first, "m-lte"),
    eq: firedIn(first, "m-eq"),
    ne: firedIn(first, "m-ne"),
    nonNumericSafe: !firedIn(first, "m-non-numeric") && clean("m-non-numeric"),
    changedInitialSafe: !firedIn(first, "m-changed") && input.changedAfterFirst === 5,
    changedStableSafe: second.checked > 0 && !firedIn(second, "m-changed"),
    changedFires: firedIn(third, "m-changed"),
  };
}

export function toolJson(result: ScriptedToolResult): Record<string, unknown> {
  const text = Array.isArray(result.content)
    ? (result.content as Array<{ text?: unknown }>).map((entry) => String(entry.text ?? "")).join("\n")
    : "";
  try {
    const parsed = JSON.parse(text) as unknown;
    if (parsed && typeof parsed === "object") return parsed as Record<string, unknown>;
  } catch {}
  return { isError: result.isError, text };
}

type LifecycleOrg = { orgId: string; memberId: string; adminId: string };

async function provisionOrg(input: {
  episode: EvalEpisode;
  model: ScriptedModelServer;
  source: EvalGraphqlSource;
  orgId: string;
  grants?: string[];
}): Promise<LifecycleOrg> {
  const { orgId } = input;
  const memberId = `member-${orgId}`;
  const adminId = `admin-${orgId}`;
  await createTestOrg(orgId, "OpenNeko lifecycle eval");
  await ensureOrgWorkspace(orgId);
  for (const [id, name] of [
    [memberId, "Eval Member"],
    [adminId, "Eval Approver"],
  ] as const) {
    await db().insert(app_user).values({ id, email: `${id}@eval.invalid`, name, org_id: orgId });
  }
  await setLocalAdministrator(orgId, adminId, true);
  await db().insert(data_source).values({
    org_id: orgId,
    kind: "graphjin",
    graphql_url: input.source.url,
    mcp_url: null,
    label: "isolated eval source",
    enabled: true,
    is_default: true,
    auth_mode: "development",
  });
  await seedProvider(orgId, {
    scope: "primary",
    provider: "openai-compatible",
    model: "scripted-lifecycle",
    enabled: true,
    config: { url: input.model.url },
    secrets: { apiKey: maybeEncryptSecret(input.episode.modelKey) },
  });
  await seedProvider(orgId, { scope: "agent", provider: "ax", enabled: true, config: { backend: "ax" } });
  if (input.grants?.length) {
    const everyone = await builtinGroupId(orgId, "everyone");
    for (const kind of input.grants) {
      await pool().query(
        `insert into item_grant (org_id, group_id, item_type, item_id) values ($1, $2, 'action', $3)`,
        [orgId, everyone, kind],
      );
    }
  }
  return { orgId, memberId, adminId };
}

export async function runScriptedTurn(
  episode: EvalEpisode,
  model: ScriptedModelServer,
  userId: string,
  question: string,
  program: ScriptedModelProgram,
  workflowId?: string,
): Promise<{ runId: string; requests: number }> {
  const feed = new RunEventFeed();
  const turn = model.arm(program, feed);
  const json = async (res: Response, what: string) => {
    const text = await res.text();
    if (!res.ok) throw new Error(`${what} answered ${res.status}: ${text}`);
    return JSON.parse(text) as Record<string, unknown>;
  };
  let runId: string;
  let done: Record<string, unknown> | undefined;
  if (workflowId) {
    const started = await json(
      await episode.api(userId, `/api/v1/workflows/${workflowId}/manual-runs`, {
        method: "POST",
        body: JSON.stringify({ userMessage: question }),
      }),
      "the manual workflow run",
    );
    runId = String(started.workRunId);
    done = await episode.followStoredRun(runId, (event) => feed.push(event));
  } else {
    const created = await json(
      await episode.api(userId, "/api/v1/work/threads", { method: "POST", body: "{}" }),
      "the thread create route",
    );
    const threadId = String((created.thread as Record<string, unknown> | undefined)?.id ?? "");
    const started = await json(
      await episode.api(userId, `/api/v1/work/threads/${threadId}/runs`, {
        method: "POST",
        body: JSON.stringify({ message: question }),
      }),
      "the run start route",
    );
    runId = String(started.runId);
    done = await episode.followRun(userId, threadId, runId, (event) => feed.push(event));
  }
  if (turn.error !== undefined) {
    throw new Error(`the scripted program failed: ${turn.error instanceof Error ? turn.error.message : String(turn.error)}`);
  }
  if (!done || done.type !== "done") throw new Error(`run ${runId} ended with no done event`);
  return { runId, requests: turn.requests.length };
}

async function decide(
  episode: EvalEpisode,
  userId: string,
  id: string,
  decision: "approve" | "reject",
): Promise<{ ok: boolean; status: number; body: Record<string, unknown> }> {
  const res = await episode.api(userId, `/api/v1/action-requests/${id}`, {
    method: "PATCH",
    body: JSON.stringify({ decision, ...(decision === "reject" ? { reason: "eval rejection" } : {}) }),
  });
  const text = await res.text();
  let body: Record<string, unknown> = {};
  try {
    body = JSON.parse(text) as Record<string, unknown>;
  } catch {}
  return { ok: res.ok, status: res.status, body };
}

async function waitForTerminal(orgId: string, id: string, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const request = await getActionRequest(orgId, id);
    if (!request || ["executed", "failed"].includes(request.status) || Date.now() > deadline) return request;
    await new Promise((wake) => setTimeout(wake, 250));
  }
}

type ScenarioContext = {
  episode: EvalEpisode;
  model: ScriptedModelServer;
  source: EvalGraphqlSource;
  orgId: string;
  turns: { count: number; requests: number };
};

async function turn(
  context: ScenarioContext,
  userId: string,
  question: string,
  program: ScriptedModelProgram,
  workflowId?: string,
): Promise<string> {
  const result = await runScriptedTurn(context.episode, context.model, userId, question, program, workflowId);
  context.turns.count += 1;
  context.turns.requests += result.requests;
  return result.runId;
}

async function conditionMatrix(context: ScenarioContext): Promise<Record<string, unknown>> {
  const { episode, source, orgId } = context;
  await provisionOrg({ ...context });
  const { workflow } = await saveWorkflow({
    orgId,
    name: "Eval condition matrix",
    steps: [{ id: "investigate", description: "Investigate the condition" }],
  });
  for (const spec of CONDITION_WATCHERS) {
    await upsertWatcher({
      orgId,
      workflowId: workflow.id,
      name: spec.name,
      query: `{ ${spec.root} { value } }`,
      valuePath: spec.valuePath,
      op: spec.op as WatcherRecord["op"],
      threshold: spec.threshold,
      cadenceSeconds: 60,
      debounceSeconds: 0,
    });
  }
  for (const [root, value] of Object.entries(CONDITION_VALUES)) source.set(root, value);
  const sweep = async (offsetMs: number) =>
    parseSweep(await episode.jobsRun("workflow_cron_sweep", orgId, new Date(SWEEP_EPOCH.getTime() + offsetMs)));
  const first = await sweep(0);
  const changedAfterFirst = (await listWatchers(orgId)).find((watcher) => watcher.name === "m-changed")?.lastValue;
  const second = await sweep(61_000);
  source.set("m_changed", 6);
  const third = await sweep(122_000);
  return conditionMatrixFrom({
    sweeps: [first, second, third],
    watchers: await listWatchers(orgId),
    changedAfterFirst,
  });
}

const THRESHOLD_WORKFLOW = "Eval threshold response";
const BROKEN_WORKFLOW = "eval-broken";

function buildWatcher(name: string, watch: Record<string, unknown>): ScriptedModelProgram {
  return async (context: ScriptedModelContext) => {
    const result = toolJson(
      await context.call("neko_workflow_builder", "create_workflow", {
        name,
        description: "Isolated deterministic watcher",
        goal: "Investigate the threshold when the watcher fires.",
        steps: [{ id: "investigate", description: "Investigate the threshold" }],
        triggers: { watch },
      }),
    );
    if (result.isError || result.triggerError) throw new Error(`the workflow builder refused: ${JSON.stringify(result)}`);
    return `Saved ${name}.`;
  };
}

async function workflowRunFires(orgId: string): Promise<Array<Record<string, unknown>>> {
  const { rows } = await pool().query<{ data: Record<string, unknown> }>(
    `select data from pgboss.job where name = 'workflow_run_fire' and data->>'orgId' = $1 order by created_on`,
    [orgId],
  );
  return rows.map((row) => row.data);
}

async function watcherLifecycle(context: ScenarioContext): Promise<Record<string, unknown>> {
  const { episode, source, orgId } = context;
  const org = await provisionOrg({ ...context });
  const watcherNamed = async (name: string) => (await listWatchers(orgId)).find((watcher) => watcher.name === name);
  const sweep = async (offsetMs: number) =>
    parseSweep(await episode.jobsRun("workflow_cron_sweep", orgId, new Date(SWEEP_EPOCH.getTime() + offsetMs)));

  await turn(context, org.memberId, "Watch the eval metric and alert above 10.", buildWatcher(THRESHOLD_WORKFLOW, {
    query: "{ eval_metric { value } }",
    value_path: "eval_metric.value",
    op: "gt",
    threshold: 10,
    cadence_seconds: 60,
    debounce_seconds: 3_600,
    severity: "high",
  }));
  const created = await watcherNamed(THRESHOLD_WORKFLOW);
  if (!created) throw new Error("the workflow builder saved no watcher");

  source.set("eval_metric", { value: 12 });
  const first = await sweep(0);
  const notDue = await sweep(1_000);
  const debounced = await sweep(61_000);
  source.set("eval_metric", { value: 13 });
  const refired = await sweep(3_662_000);
  const fireCount = [first, notDue, debounced, refired].reduce(
    (total, result) => total + result.fired.filter((entry) => entry.watcherId === created.id).length,
    0,
  );
  const fires = await workflowRunFires(orgId);
  const firstFire = fires[0];

  await turn(context, org.memberId, "Change the eval watcher to 11 or more, critical.", buildWatcher(THRESHOLD_WORKFLOW, {
    query: "{ eval_metric { value } }",
    value_path: "eval_metric.value",
    op: "gte",
    threshold: 11,
    cadence_seconds: 120,
    debounce_seconds: 3_600,
    severity: "critical",
  }));
  const updated = await watcherNamed(THRESHOLD_WORKFLOW);

  const disabled = await setWatcherEnabled(orgId, created.id, false);
  source.set("eval_metric", { value: 99 });
  const disabledSweep = await sweep(8_000_000);
  const reset = await resetWatcher(orgId, created.id);
  const removed = await episode.api(org.memberId, `/api/v1/workflows/${created.workflowId}`, { method: "DELETE" });
  await removed.arrayBuffer();
  const afterDelete = (await listWatchers(orgId)).find((watcher) => watcher.id === created.id) ?? null;

  await turn(context, org.memberId, "Watch the unavailable metric.", buildWatcher(BROKEN_WORKFLOW, {
    query: "{ unavailable { value } }",
    value_path: "unavailable.value",
    op: "gt",
    threshold: 0,
    cadence_seconds: 60,
  }));
  source.set("unavailable", new Error("eval transport unavailable"));
  const errorSweep = await sweep(9_000_000);
  const brokenState = await watcherNamed(BROKEN_WORKFLOW);

  return {
    created: created.enabled && created.op === "gt",
    firstFire: first.checked === 1 && first.fired.length === 1,
    evidencePreserved:
      firstFire?.workflowId === created.workflowId &&
      (firstFire?.triggerPayload as Record<string, unknown> | undefined)?.value === 12,
    cadenceHonored: notDue.checked === 0,
    debounceHonored: debounced.checked === 1 && debounced.fired.length === 0,
    refiresAfterWindow: refired.fired.length === 1 && fireCount === 2,
    updated:
      updated?.id === created.id &&
      updated.op === "gte" &&
      updated.threshold === 11 &&
      updated.severity === "critical",
    disabled: disabled?.enabled === false && disabledSweep.checked === 0,
    reset:
      reset?.lastCheckedAt === null &&
      reset.lastFiredAt === null &&
      reset.lastValue === null &&
      reset.lastError === null,
    deleted: removed.ok && afterDelete === null,
    errorRecorded:
      errorSweep.checked === 1 &&
      errorSweep.fired.length === 0 &&
      /transport unavailable/u.test(brokenState?.lastError ?? ""),
    noLeakedDefinitions: (await listWatchers(orgId)).length === 1,
  };
}

type PolicyProbe = {
  key: string;
  scope: "internal" | "external";
  kind: string;
  target?: string;
  risk_level?: string;
};

export const POLICY_PROBES: readonly PolicyProbe[] = [
  { key: "low", scope: "external", kind: ACTION_KIND, target: "account-1", risk_level: "low" },
  { key: "high", scope: "external", kind: ACTION_KIND, target: "account-1", risk_level: "high" },
  { key: "denied", scope: "external", kind: ACTION_KIND, target: "account-blocked", risk_level: "low" },
  { key: "wrongTarget", scope: "external", kind: ACTION_KIND, target: "contact-1", risk_level: "low" },
  { key: "priority", scope: "external", kind: PRIORITY_KIND, target: "account-1", risk_level: "low" },
  { key: "absent", scope: "internal", kind: "unmatched" },
];

export function policyMatrixFrom(
  results: Record<string, Record<string, unknown>>,
  decisions: Record<string, unknown>,
): Record<string, unknown> {
  return {
    lowRiskAutoApproved: decisions.low === "allow" && results.low?.decision === "auto_approved",
    highRiskNeedsApproval: decisions.high === "needs_approval" && results.high?.decision === "needs_approval",
    deniedTargetRejected: decisions.denied === "deny" && results.denied?.decision === "denied",
    outsideAllowlistRejected: decisions.wrongTarget === "deny" && results.wrongTarget?.decision === "denied",
    priorityHonored: decisions.priority === "deny" && results.priority?.policy === "deny-first",
    noPolicyExplicit: decisions.absent === "no_policy" && results.absent?.decision === "denied",
  };
}

async function actionWorkflow(orgId: string, name: string): Promise<string> {
  const { workflow } = await saveWorkflow({
    orgId,
    name,
    description: "Propose the eval actions.",
    goal: "Propose each eval action once through neko_action.",
    steps: [{ id: "propose", description: "Request each eval action." }],
  });
  return workflow.id;
}

function basePolicy(orgId: string, kind: string, patch: Partial<Parameters<typeof createActionPolicy>[0]> = {}) {
  return createActionPolicy({
    orgId,
    name: `eval-policy-${kind}`,
    description: "",
    appliesToKinds: [kind],
    appliesToScopes: ["external"],
    mode: "auto_approve",
    riskThresholdAutoApprove: "medium",
    allowedTargets: { patterns: ["account-*"] },
    deniedTargets: { patterns: ["account-blocked"] },
    limits: {},
    approverRole: null,
    priority: 100,
    enabled: true,
    ...patch,
  });
}

async function policyMatrix(context: ScenarioContext): Promise<Record<string, unknown>> {
  const { episode, orgId } = context;
  const org = await provisionOrg({ ...context, grants: [ACTION_KIND, PRIORITY_KIND] });
  await basePolicy(orgId, ACTION_KIND);
  await basePolicy(orgId, PRIORITY_KIND);
  await basePolicy(orgId, PRIORITY_KIND, { name: "deny-first", mode: "never", priority: 10 });
  registerActionAdapter(ACTION_KIND, async () => ({ result: { mocked: true } }));
  const workflowId = await actionWorkflow(orgId, "Eval policy probes");
  const results: Record<string, Record<string, unknown>> = {};
  const runId = await turn(context, org.adminId, "Try each eval action once.", async (model) => {
    for (const probe of POLICY_PROBES) {
      const { key, ...args } = probe;
      results[key] = toolJson(
        await model.call("neko_action", "request", { ...args, payload: { status: "reviewed" }, summary: `Eval probe ${key}` }),
      );
    }
    return "Tried each eval action.";
  }, workflowId);
  const trace = await episode.readTrace(runId);
  const policyEvents = trace.events.filter((event) => event.operation === "action.policy");
  const decisions = Object.fromEntries(
    POLICY_PROBES.map((probe, index) => [
      probe.key,
      (policyEvents[index]?.evidence as Record<string, unknown> | undefined)?.decision,
    ]),
  );
  return policyMatrixFrom(results, decisions);
}

async function actionLifecycle(context: ScenarioContext): Promise<Record<string, unknown>> {
  const { episode, orgId } = context;
  const org = await provisionOrg({ ...context, grants: [ACTION_KIND] });
  const policyRecord = await createActionPolicy({
    orgId,
    name: "eval-approval",
    description: "Require approval for the isolated eval mutation",
    appliesToKinds: [ACTION_KIND],
    appliesToScopes: ["external"],
    mode: "approval_required",
    riskThresholdAutoApprove: null,
    allowedTargets: { patterns: ["account-*"] },
    deniedTargets: null,
    limits: {},
    approverRole: "admin",
    priority: 10,
    enabled: true,
  });
  const ledger = new Map<string, string>([
    ["account-1", "pending"],
    ["account-2", "stable"],
  ]);
  let effects = 0;
  registerActionAdapter(ACTION_KIND, async ({ request }) => {
    effects += 1;
    ledger.set(String(request.target), String(request.payload.status));
    return {
      commandOrOperation: "eval:ledger.update",
      externalRef: `eval:${request.id}`,
      result: { updated: true },
    };
  });
  const workflowId = await actionWorkflow(orgId, "Eval ledger review");
  const proposals: Record<string, Record<string, unknown>> = {};
  await turn(context, org.adminId, "Mark account-1 reviewed and propose a change to account-2.", async (model) => {
    for (const [key, target, status] of [
      ["approve", "account-1", "reviewed"],
      ["reject", "account-2", "must-not-change"],
    ] as const) {
      proposals[key] = toolJson(
        await model.call("neko_action", "request", {
          scope: "external",
          kind: ACTION_KIND,
          target,
          payload: { status },
          risk_level: "medium",
          summary: `Set ${target} to ${status}`,
        }),
      );
    }
    return "Proposed both changes.";
  }, workflowId);
  const requestId = String(proposals.approve?.actionRequestId ?? "");
  const rejectedId = String(proposals.reject?.actionRequestId ?? "");
  const request = requestId ? await getActionRequest(orgId, requestId) : null;
  if (!request || !rejectedId) throw new Error(`the turn stored no action request: ${JSON.stringify(proposals)}`);

  const outsider = await decide(episode, org.memberId, requestId, "approve");
  const approved = await decide(episode, org.adminId, requestId, "approve");
  const duplicate = await decide(episode, org.adminId, requestId, "approve");
  const terminal = await waitForTerminal(orgId, requestId);
  const executions = await listActionExecutions(requestId);

  const rejected = await decide(episode, org.adminId, rejectedId, "reject");
  const approveAfterReject = await decide(episode, org.adminId, rejectedId, "approve");
  await new Promise((wake) => setTimeout(wake, 1_000));
  const rejectedState = await getActionRequest(orgId, rejectedId);
  const audit = await verifyAuditChain(orgId);
  registerActionAdapter(ACTION_KIND, async () => ({ result: { mocked: true } }));

  return {
    proposed: request.status === "pending_approval" && request.policyId === policyRecord.id,
    serviceDenied: !outsider.ok && outsider.status === 403,
    approved: approved.ok && (approved.body.actionRequest as Record<string, unknown> | undefined)?.status === "approved",
    duplicateApprovalDenied: !duplicate.ok,
    executed: terminal?.status === "executed" && ledger.get("account-1") === "reviewed",
    receipt:
      executions.length === 1 &&
      executions[0]?.status === "succeeded" &&
      executions[0]?.externalRef === `eval:${requestId}`,
    atMostOnce: effects === 1 && !duplicate.ok,
    collateralUnchanged: ledger.get("account-2") === "stable",
    rejectionTerminal:
      rejected.ok && rejectedState?.status === "rejected" && !approveAfterReject.ok && effects === 1,
    auditValid: audit.ok && audit.length >= 5,
  };
}

const SCENARIOS: Record<string, (context: ScenarioContext) => Promise<Record<string, unknown>>> = {
  "watcher-condition-matrix": conditionMatrix,
  "watcher-lifecycle": watcherLifecycle,
  "action-policy-matrix": policyMatrix,
  "action-lifecycle": actionLifecycle,
};

export function createOpenNekoLifecycleDriver(
  _context: { loaded: LoadedEval; plan: EvalPlan },
  options: { host?: EvalInstallHostOptions } = {},
): EvalDriver {
  let host: EvalInstallHost | undefined;
  let model: ScriptedModelServer | undefined;
  let source: EvalGraphqlSource | undefined;
  return {
    id: "openneko.lifecycle",
    scorer: SCORER,
    async preflight({ loaded, plan }) {
      if (!(await dbReachable())) {
        throw new EvalEnvironmentError(
          "OpenNeko PostgreSQL is unreachable; lifecycle evals require the migrated control-plane database",
          "openneko_database_unreachable",
        );
      }
      if (plan.slots.some((slot) => slot.variant.data_path !== "none")) {
        throw new EvalEnvironmentError(
          "openneko.lifecycle requires data_path: none",
          "variant_incompatible",
        );
      }
      if (loaded.config.defaults.concurrency !== 1) {
        throw new EvalEnvironmentError(
          "openneko.lifecycle requires concurrency: 1, because one scripted model serves every turn",
          "unsafe_concurrency",
        );
      }
      const allowedEffects = new Set([
        "isolated-test-org",
        "in-memory-action-adapter",
      ]);
      for (const evalCase of loaded.cases) {
        if (
          (evalCase.family === "watcher" || evalCase.family === "mutation") &&
          evalCase.allowed_side_effects.some((item) => !allowedEffects.has(item))
        ) {
          throw new EvalEnvironmentError(
            `${evalCase.id} declares an unsupported side effect`,
            "unsafe_side_effect",
          );
        }
      }
      const fingerprint = await pool().query<{
        database_name: string;
        server_version: string;
        schema_digest: string;
      }>(`
        select current_database() as database_name,
               current_setting('server_version') as server_version,
               (select md5(string_agg(table_name || ':' || column_name || ':' || data_type, ',' order by table_name, ordinal_position))
                  from information_schema.columns
                 where table_schema = 'public'
                   and table_name in ('watcher', 'action_request', 'action_execution', 'action_policy', 'audit_chain')) as schema_digest
      `);
      host ??= await EvalInstallHost.prepare(options.host);
      model ??= await ScriptedModelServer.start();
      source ??= await EvalGraphqlSource.start();
      return {
        datasetFingerprint: {
          dataset: loaded.cases[0]?.dataset,
          snapshot: loaded.datasetSnapshots.get(loaded.cases[0]?.dataset ?? ""),
          ...fingerprint.rows[0],
        },
        resolvedVariants: Object.fromEntries(
          loaded.config.variants.map((variant) => [
            variant.id,
            {
              backend: "openneko-install-scripted-model",
              modelCalls: 0,
              dataPath: variant.data_path,
            },
          ]),
        ),
      };
    },
    async resolveOracle(evalCase) {
      if (evalCase.oracle?.kind !== "inline.expected") {
        throw new EvalEnvironmentError(
          `${evalCase.id} requires inline.expected oracle`,
          "oracle_invalid",
        );
      }
      return evalCase.oracle.params ?? {};
    },
    async reset() {},
    async execute({ runId, slot, observer, signal }) {
      if (signal.aborted) throw signal.reason;
      if (!host || !model || !source) {
        throw new EvalEnvironmentError("the eval install is not prepared", "backend_runtime_missing");
      }
      const scenario = String(slot.case.input.scenario ?? "");
      const run = SCENARIOS[scenario];
      if (!run) throw new Error(`unsupported lifecycle scenario ${scenario}`);
      const operationId = `lifecycle:${shortDigest(slot.key)}`;
      const started = Date.now();
      await observer.observe({
        kind: "run.start",
        operationId,
        attributes: {
          "openneko.run.kind": "eval",
          "openneko.product.path": slot.case.product_path,
          "openneko.eval.case.id": slot.caseId,
          "openneko.eval.variant.id": slot.variantId,
        },
      });
      const stage = scenario.startsWith("watcher-") ? "stage" : scenario === "action-policy-matrix" ? "policy" : "approval";
      if (stage === "policy" || stage === "approval") {
        await observer.observe({
          kind: stage === "policy" ? "policy.decision" : "approval.decision",
          operationId: `${operationId}:${stage}`,
          parentOperationId: operationId,
          status: "ok",
        });
      } else {
        await observer.observe({
          kind: "stage.start",
          operationId: `${operationId}:watcher`,
          parentOperationId: operationId,
          attributes: { "openneko.stage": scenario },
        });
      }
      source.clear();
      const episode = await host.startEpisode({ label: slot.caseId });
      const turns = { count: 0, requests: 0 };
      try {
        episode.activate();
        const output = await run({
          episode,
          model,
          source,
          orgId: `eval-lifecycle-${shortDigest(`${runId}/${slot.key}`).slice(0, 16)}`,
          turns,
        });
        if (stage === "stage") {
          await observer.observe({
            kind: "stage.end",
            operationId: `${operationId}:watcher`,
            parentOperationId: operationId,
            status: "ok",
          });
        }
        const wallDurationMs = Date.now() - started;
        await observer.observe({
          kind: "run.end",
          operationId,
          status: "ok",
          attributes: { "openneko.outcome": "completed" },
          measurements: { durationMs: wallDurationMs, coverage: "unavailable" },
        });
        return {
          output,
          measurements: {
            wallDurationMs,
            modelCalls: 0,
            scriptedTurns: turns.count,
            scriptedModelRequests: turns.requests,
            sourceQueries: source.queries.length,
          },
        };
      } catch (cause) {
        await observer.observe({
          kind: "run.end",
          operationId,
          status: "error",
          errorType: cause instanceof Error ? cause.name : "unknown",
          attributes: { "openneko.outcome": "failed" },
          measurements: {
            durationMs: Date.now() - started,
            coverage: "unavailable",
          },
        });
        throw cause;
      } finally {
        await episode.stop();
      }
    },
    score({ case: evalCase, oracle, execution }) {
      const output = execution.output;
      const expected = (oracle as { expected?: Record<string, unknown> }).expected ?? {};
      const checks = evalCase.assertions.map((assertion) => {
        const path = String(assertion.params?.path ?? "");
        const actual = valueAtPath(output, path);
        const wanted =
          assertion.params && "expected" in assertion.params
            ? assertion.params.expected
            : expected[path] ?? true;
        const passed = Object.is(actual, wanted);
        return {
          assertionId: assertion.id,
          dimension: assertion.dimension,
          passed,
          gate: assertion.gate,
          diagnostic: passed
            ? `path=${path}`
            : `path=${path} expected=${JSON.stringify(wanted)} actual=${JSON.stringify(actual)}`,
        };
      });
      return createScore({
        scorerId: SCORER.id,
        scorerVersion: SCORER.version,
        scorerDefinition: SCORER.definition,
        checks,
      });
    },
    async close() {
      await host?.close();
      await model?.close();
      await source?.close();
      host = undefined;
      model = undefined;
      source = undefined;
      await pool().end();
    },
  };
}
