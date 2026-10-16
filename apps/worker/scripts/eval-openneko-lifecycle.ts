import {
  db,
  pool,
  watcher,
  eq,
} from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedDataSource,
} from "@neko/db/test-helpers";
import {
  EvalEnvironmentError,
  createScore,
  shortDigest,
  type EvalDriver,
  type EvalExecution,
  type EvalPlan,
  type LoadedEval,
} from "@neko/evals";
import {
  approveActionRequest,
  createActionPolicy,
  createActionRequest,
  deleteWatcher,
  evaluateActionPolicy,
  executeApprovedActionRequest,
  extractValueAtPath,
  getActionRequest,
  getWatcher,
  listActionExecutions,
  listWatchers,
  registerActionAdapter,
  rejectActionRequest,
  resetWatcher,
  saveWorkflow,
  setWatcherEnabled,
  sweepWatchers,
  upsertWatcher,
  verifyAuditChain,
  watcherConditionMet,
  type ActionPolicyRecord,
} from "@neko/llm/workflows";

const ACTION_KIND = "openneko_eval_mutation";
const SCORER = {
  id: "openneko.lifecycle",
  version: "1.0.0",
  definition: {
    assertions: "exact deterministic field checks against product-path state",
    sideEffects: "isolated organization plus in-memory action adapter",
  },
} as const;

function orgIdFor(runId: string, slotKey: string): string {
  return `eval-lifecycle-${shortDigest(`${runId}/${slotKey}`).slice(0, 16)}`;
}

function valueAtPath(value: unknown, path: string): unknown {
  return path
    .split(".")
    .filter(Boolean)
    .reduce<unknown>((current, segment) => {
      if (!current || typeof current !== "object") return undefined;
      return (current as Record<string, unknown>)[segment];
    }, value);
}

function policy(
  patch: Partial<ActionPolicyRecord> = {},
): ActionPolicyRecord {
  const now = new Date("2026-01-01T00:00:00.000Z");
  return {
    id: "policy-eval",
    orgId: "eval",
    name: "eval-policy",
    description: "",
    appliesToKinds: [ACTION_KIND],
    appliesToScopes: ["external"],
    mode: "auto_approve",
    riskThresholdAutoApprove: "medium",
    allowedTargets: { patterns: ["account-*"] },
    deniedTargets: { patterns: ["account-blocked"] },
    limits: {},
    approverGroupId: null,
    priority: 100,
    enabled: true,
    createdByThreadId: null,
    createdByRunId: null,
    createdAt: now,
    updatedAt: now,
    ...patch,
  };
}

function conditionMatrix(): Record<string, unknown> {
  const data = { orders: [{ count: 7 }], status: "P0" };
  return {
    pathObjectArray: extractValueAtPath(data, "orders.0.count") === 7,
    pathMissing: extractValueAtPath(data, "orders.1.count") === undefined,
    gt: watcherConditionMet("gt", 5, 3, null),
    gte: watcherConditionMet("gte", 3, 3, null),
    lt: watcherConditionMet("lt", 2, 3, null),
    lte: watcherConditionMet("lte", 3, 3, null),
    eq: watcherConditionMet("eq", "P0", "P0", null),
    ne: watcherConditionMet("ne", "P1", "P0", null),
    nonNumericSafe: !watcherConditionMet("gt", "not-a-number", 3, null),
    changedInitialSafe: !watcherConditionMet("changed", 5, null, undefined),
    changedStableSafe: !watcherConditionMet("changed", 5, null, 5),
    changedFires: watcherConditionMet("changed", 6, null, 5),
  };
}

function policyMatrix(): Record<string, unknown> {
  const base = policy();
  const low = evaluateActionPolicy(
    {
      scope: "external",
      kind: ACTION_KIND,
      target: "account-1",
      riskLevel: "low",
    },
    [base],
  );
  const high = evaluateActionPolicy(
    {
      scope: "external",
      kind: ACTION_KIND,
      target: "account-1",
      riskLevel: "high",
    },
    [base],
  );
  const denied = evaluateActionPolicy(
    {
      scope: "external",
      kind: ACTION_KIND,
      target: "account-blocked",
      riskLevel: "low",
    },
    [base],
  );
  const wrongTarget = evaluateActionPolicy(
    {
      scope: "external",
      kind: ACTION_KIND,
      target: "contact-1",
      riskLevel: "low",
    },
    [base],
  );
  const priority = evaluateActionPolicy(
    {
      scope: "external",
      kind: ACTION_KIND,
      target: "account-1",
      riskLevel: "low",
    },
    [base, policy({ id: "deny-first", mode: "never", priority: 10 })],
  );
  const absent = evaluateActionPolicy(
    { scope: "internal", kind: "unmatched" },
    [base],
  );
  return {
    lowRiskAutoApproved: low.decision === "allow",
    highRiskNeedsApproval: high.decision === "needs_approval",
    deniedTargetRejected: denied.decision === "deny",
    outsideAllowlistRejected: wrongTarget.decision === "deny",
    priorityHonored:
      priority.decision === "deny" && priority.policy.id === "deny-first",
    noPolicyExplicit: absent.decision === "no_policy",
  };
}

async function watcherLifecycle(orgId: string): Promise<Record<string, unknown>> {
  await createTestOrg(orgId, "OpenNeko watcher eval");
  await seedDataSource(orgId, {
    graphqlUrl: "http://eval.invalid/api/v1/graphql",
    mcpUrl: null,
    label: "isolated eval source",
  });
  const { workflow } = await saveWorkflow({
    orgId,
    name: "Eval threshold response",
    steps: [{ id: "investigate", description: "Investigate the threshold" }],
  });
  const created = await upsertWatcher({
    orgId,
    workflowId: workflow.id,
    name: "eval-threshold",
    description: "Isolated deterministic watcher",
    query: "{ eval_metric { value } }",
    valuePath: "eval_metric.value",
    op: "gt",
    threshold: 10,
    cadenceSeconds: 60,
    debounceSeconds: 3_600,
    severity: "high",
  });
  const fires: Array<Record<string, unknown>> = [];
  const t0 = new Date("2026-01-01T00:00:00.000Z");
  const sweep = (now: Date, value: number) =>
    sweepWatchers(orgId, {
      now: () => now,
      query: async <T = unknown>() => ({
        data: { eval_metric: { value } } as T,
      }),
      enqueueFire: async (payload) => {
        fires.push(payload);
      },
    });
  const first = await sweep(t0, 12);
  const notDue = await sweep(new Date(t0.getTime() + 1_000), 12);
  const debounced = await sweep(new Date(t0.getTime() + 61_000), 12);
  const refired = await sweep(new Date(t0.getTime() + 3_662_000), 13);

  const updated = await upsertWatcher({
    orgId,
    workflowId: workflow.id,
    name: created.name,
    description: "Updated definition",
    query: "{ eval_metric { value } }",
    valuePath: "eval_metric.value",
    op: "gte",
    threshold: 11,
    cadenceSeconds: 120,
    debounceSeconds: 3_600,
    severity: "critical",
  });
  const disabled = await setWatcherEnabled(orgId, created.id, false);
  const disabledSweep = await sweep(
    new Date(t0.getTime() + 8_000_000),
    99,
  );
  const reset = await resetWatcher(orgId, created.id);
  const deleted = await deleteWatcher(orgId, created.id);
  const afterDelete = await getWatcher(orgId, created.id);

  const broken = await upsertWatcher({
    orgId,
    workflowId: workflow.id,
    name: "eval-broken",
    query: "{ unavailable }",
    valuePath: "unavailable.value",
    op: "gt",
    threshold: 0,
    cadenceSeconds: 60,
  });
  const errorSweep = await sweepWatchers(orgId, {
    now: () => new Date(t0.getTime() + 9_000_000),
    query: async () => {
      throw new Error("eval transport unavailable");
    },
    enqueueFire: async () => {
      throw new Error("errored watcher must not fire");
    },
  });
  const brokenState = await getWatcher(orgId, broken.id);

  return {
    created: created.enabled && created.op === "gt",
    firstFire: first.checked === 1 && first.fired.length === 1,
    evidencePreserved:
      fires[0]?.workflowId === workflow.id &&
      (fires[0]?.triggerPayload as Record<string, unknown> | undefined)?.value ===
        12,
    cadenceHonored: notDue.checked === 0,
    debounceHonored: debounced.checked === 1 && debounced.fired.length === 0,
    refiresAfterWindow: refired.fired.length === 1 && fires.length === 2,
    updated:
      updated.id === created.id &&
      updated.op === "gte" &&
      updated.threshold === 11 &&
      updated.severity === "critical",
    disabled: disabled?.enabled === false && disabledSweep.checked === 0,
    reset:
      reset?.lastCheckedAt === null &&
      reset.lastFiredAt === null &&
      reset.lastValue === null &&
      reset.lastError === null,
    deleted: deleted && afterDelete === null,
    errorRecorded:
      errorSweep.checked === 1 &&
      errorSweep.fired.length === 0 &&
      /transport unavailable/u.test(brokenState?.lastError ?? ""),
    noLeakedDefinitions: (await listWatchers(orgId)).length === 1,
  };
}

async function actionLifecycle(orgId: string): Promise<Record<string, unknown>> {
  await createTestOrg(orgId, "OpenNeko action eval");
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
  const request = await createActionRequest({
    orgId,
    policyId: policyRecord.id,
    scope: "external",
    kind: ACTION_KIND,
    target: "account-1",
    payload: { status: "reviewed" },
    riskLevel: "medium",
    status: "pending_approval",
    summary: "Mark the isolated eval account reviewed",
    intent: "Verify approval-governed mutation semantics",
    actorUserId: "eval-member",
    actorRole: "member",
    actorBackend: "eval-driver",
  });
  let serviceDenied = false;
  try {
    await approveActionRequest({
      id: request.id,
      orgId,
      approverUserId: null,
      approver: { userId: null, role: "service" },
    });
  } catch {
    serviceDenied = true;
  }
  const approved = await approveActionRequest({
    id: request.id,
    orgId,
    approverUserId: null,
    approver: { userId: null, role: "admin" },
  });
  let duplicateApprovalDenied = false;
  try {
    await approveActionRequest({
      id: request.id,
      orgId,
      approverUserId: null,
    });
  } catch {
    duplicateApprovalDenied = true;
  }
  const executed = await executeApprovedActionRequest(orgId, request.id);
  let duplicateExecutionDenied = false;
  try {
    await executeApprovedActionRequest(orgId, request.id);
  } catch {
    duplicateExecutionDenied = true;
  }
  const terminal = await getActionRequest(orgId, request.id);
  const executions = await listActionExecutions(request.id);

  const rejectedRequest = await createActionRequest({
    orgId,
    policyId: policyRecord.id,
    scope: "external",
    kind: ACTION_KIND,
    target: "account-2",
    payload: { status: "must-not-change" },
    status: "pending_approval",
  });
  const rejected = await rejectActionRequest({
    id: rejectedRequest.id,
    orgId,
    approverUserId: null,
    reason: "eval rejection",
    approver: { userId: null, role: "admin" },
  });
  let rejectedExecutionDenied = false;
  try {
    await executeApprovedActionRequest(orgId, rejected.id);
  } catch {
    rejectedExecutionDenied = true;
  }
  const audit = await verifyAuditChain(orgId);

  // Leave a harmless adapter behind if another in-process test uses the kind.
  registerActionAdapter(ACTION_KIND, async () => ({ result: { mocked: true } }));
  return {
    proposed: request.status === "pending_approval" && request.policyId === policyRecord.id,
    serviceDenied,
    approved: approved.status === "approved",
    duplicateApprovalDenied,
    executed:
      executed.ok && terminal?.status === "executed" && ledger.get("account-1") === "reviewed",
    receipt:
      executions.length === 1 &&
      executions[0]?.status === "succeeded" &&
      executions[0]?.externalRef === `eval:${request.id}`,
    atMostOnce: effects === 1 && duplicateExecutionDenied,
    collateralUnchanged: ledger.get("account-2") === "stable",
    rejectionTerminal:
      rejected.status === "rejected" && rejectedExecutionDenied && effects === 1,
    auditValid: audit.ok && audit.length >= 5,
  };
}

export function createOpenNekoLifecycleDriver(context: {
  loaded: LoadedEval;
  plan: EvalPlan;
}): EvalDriver {
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
              backend: "deterministic-product-runtime",
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
    async reset({ runId, slot }) {
      const orgId = orgIdFor(runId, slot.key);
      await deleteTestOrg(orgId);
      registerActionAdapter(ACTION_KIND, async () => ({ result: { mocked: true } }));
    },
    async execute({ runId, slot, observer, signal }) {
      if (signal.aborted) throw signal.reason;
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
      try {
        const scenario = String(slot.case.input.scenario ?? "");
        let output: Record<string, unknown>;
        if (scenario === "watcher-condition-matrix") {
          output = conditionMatrix();
        } else if (scenario === "action-policy-matrix") {
          await observer.observe({
            kind: "policy.decision",
            operationId: `${operationId}:policy`,
            parentOperationId: operationId,
            status: "ok",
          });
          output = policyMatrix();
        } else if (scenario === "watcher-lifecycle") {
          await observer.observe({
            kind: "stage.start",
            operationId: `${operationId}:watcher`,
            parentOperationId: operationId,
            attributes: { "openneko.stage": "watcher-lifecycle" },
          });
          output = await watcherLifecycle(orgIdFor(runId, slot.key));
          await observer.observe({
            kind: "stage.end",
            operationId: `${operationId}:watcher`,
            parentOperationId: operationId,
            status: "ok",
          });
        } else if (scenario === "action-lifecycle") {
          await observer.observe({
            kind: "approval.decision",
            operationId: `${operationId}:approval`,
            parentOperationId: operationId,
            status: "ok",
          });
          output = await actionLifecycle(orgIdFor(runId, slot.key));
        } else {
          throw new Error(`unsupported lifecycle scenario ${scenario}`);
        }
        const wallDurationMs = Date.now() - started;
        await observer.observe({
          kind: "run.end",
          operationId,
          status: "ok",
          attributes: { "openneko.outcome": "completed" },
          measurements: { durationMs: wallDurationMs, coverage: "unavailable" },
        });
        return { output, measurements: { wallDurationMs, modelCalls: 0 } };
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
      // CLI adapters own this process's shared DB pool.
      await pool().end();
    },
  };
}
