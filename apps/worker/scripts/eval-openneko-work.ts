import { existsSync } from "node:fs";
import { readdir } from "node:fs/promises";
import { join } from "node:path";
import { app_user, db, pool, setLocalAdministrator } from "@neko/db";
import { createTestOrg, dbReachable, seedProvider } from "@neko/db/test-helpers";
import {
  EvalEnvironmentError,
  assertionsForPhase,
  createScore,
  oracleSpecForPhase,
  shortDigest,
  type EvalDriver,
  type EvalPlan,
  type LoadedCase,
  type LoadedEval,
} from "@neko/evals";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import {
  KNOWN_SKILL_DEPS,
  aggregateSkillDeps,
  ensureOrgWorkspace,
  findConflictingWorkMemories,
  parseSkillFrontmatter,
} from "@neko/llm/work";
import { EvalInstallHost, type EvalEpisode, type EvalInstallHostOptions } from "./eval-install";
import { runScriptedTurn, toolJson } from "./eval-openneko-lifecycle";
import { ScriptedModelServer, type ScriptedModelContext, type ScriptedModelProgram } from "./eval-scripted-model";

const SCORER = {
  id: "openneko.work-memory",
  version: "1.0.0",
  definition: {
    assertions:
      "exact deterministic field checks against memory retrieval, memory " +
      "lifecycle, and skill install/round-trip product seams",
    sideEffects: "isolated organizations plus a temporary skills root",
  },
} as const;

const PLANTED_SLA = {
  kind: "business_rule",
  text: "Revenue alert SLA: flag any sales territory when its weekly order value drops below 18500 USD.",
} as const;
const PLANTED_DEFINITION = {
  kind: "metric_definition",
  text: "Active customer: a customer with at least one sales order in the trailing 12 months.",
} as const;
const DECOY = {
  kind: "preference",
  text: "The Friday team lunch budget is 240 USD and pizza is preferred.",
} as const;

const SLA_QUERY = "revenue alert SLA threshold";
const DEFINITION_QUERY = "active customer definition";
const MISS_QUERY = "quarterly carbon emissions audit cadence";
const DECOY_QUERY = "team lunch budget";

export type MemoryHit = {
  source: string;
  score: number;
  memory: { id: string; text: string; useCount?: number };
};

type WorkOrg = { orgId: string; adminId: string };

type Seeded = { planted: string; definition: string; decoy: string };

type WorkContext = {
  episode: EvalEpisode;
  model: ScriptedModelServer;
  orgId: string;
  turns: { count: number; requests: number };
};

async function provisionOrg(context: WorkContext, orgId: string): Promise<WorkOrg> {
  const adminId = `admin-${orgId}`;
  await createTestOrg(orgId, "OpenNeko work-memory eval");
  await ensureOrgWorkspace(orgId);
  await db().insert(app_user).values({ id: adminId, email: `${adminId}@eval.invalid`, name: "Eval Admin", org_id: orgId });
  await setLocalAdministrator(orgId, adminId, true);
  await seedProvider(orgId, {
    scope: "primary",
    provider: "openai-compatible",
    model: "scripted-work",
    enabled: true,
    config: { url: context.model.url },
    secrets: { apiKey: maybeEncryptSecret(context.episode.modelKey) },
  });
  await seedProvider(orgId, { scope: "agent", provider: "ax", enabled: true, config: { backend: "ax" } });
  return { orgId, adminId };
}

async function turn(context: WorkContext, org: WorkOrg, question: string, program: ScriptedModelProgram): Promise<void> {
  const result = await runScriptedTurn(context.episode, context.model, org.adminId, question, program);
  context.turns.count += 1;
  context.turns.requests += result.requests;
}

async function apiJson(
  context: WorkContext,
  org: WorkOrg,
  path: string,
  init: RequestInit = {},
): Promise<{ status: number; body: Record<string, unknown> }> {
  const res = await context.episode.api(org.adminId, path, init);
  const text = await res.text();
  let body: Record<string, unknown> = {};
  try {
    body = JSON.parse(text) as Record<string, unknown>;
  } catch {}
  return { status: res.status, body };
}

async function storedMemory(context: WorkContext, org: WorkOrg, id: string): Promise<Record<string, unknown> | undefined> {
  const { status, body } = await apiJson(context, org, `/api/v1/work/memories/${id}`);
  return status === 200 ? (body.memory as Record<string, unknown>) : undefined;
}

async function searchMemory(context: ScriptedModelContext, query: string): Promise<MemoryHit[]> {
  const result = toolJson(await context.call("neko_memory", "search", { query, limit: 10 }));
  if (!Array.isArray(result.results)) throw new Error(`the memory search refused: ${JSON.stringify(result)}`);
  return result.results as MemoryHit[];
}

async function searchTurn(context: WorkContext, org: WorkOrg, queries: string[]): Promise<MemoryHit[][]> {
  const found: MemoryHit[][] = [];
  await turn(context, org, "Look up what we saved about these topics.", async (model) => {
    for (const query of queries) found.push(await searchMemory(model, query));
    return "Done.";
  });
  return found;
}

async function seed(context: WorkContext, org: WorkOrg): Promise<Seeded> {
  const ids: string[] = [];
  await turn(context, org, "Remember these three facts.", async (model) => {
    for (const draft of [PLANTED_SLA, PLANTED_DEFINITION, DECOY]) {
      const result = toolJson(
        await model.call("neko_memory", "save", { text: draft.text, kind: draft.kind, scope: "global", pinned: false }),
      );
      if (typeof result.memoryId !== "string") throw new Error(`the memory save refused: ${JSON.stringify(result)}`);
      ids.push(result.memoryId);
    }
    return "Saved.";
  });
  const [planted, definition, decoy] = ids as [string, string, string];
  return { planted, definition, decoy };
}

const texts = (hits: MemoryHit[]) => hits.map((hit) => hit.memory.text);
const savedOnly = (...lists: MemoryHit[][]) => lists.every((hits) => hits.every((hit) => hit.source === "saved_memory"));

export function searchOutputFrom(input: {
  seeded: Seeded;
  hit: MemoryHit[];
  miss: MemoryHit[];
  plantedUseCount: number | undefined;
}): Record<string, unknown> {
  const { seeded, hit, miss } = input;
  const hitScore = hit.find((result) => result.memory.id === seeded.planted)?.score;
  const decoyScore = hit.find((result) => result.memory.id === seeded.decoy)?.score;
  const topMissScore = miss[0]?.score;
  return {
    plantedFound: texts(hit).includes(PLANTED_SLA.text),
    plantedRankedFirst: hit[0]?.memory.id === seeded.planted,
    decoyOutranked: hitScore !== undefined && (decoyScore === undefined || hitScore > decoyScore),
    missWeakerThanHit: hitScore !== undefined && (topMissScore === undefined || topMissScore < hitScore),
    archivesEmpty: savedOnly(hit, miss),
    usageTouched: (input.plantedUseCount ?? 0) >= 1,
  };
}

async function seedPhase(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const seeded = await seed(context, org);
  const planted = await storedMemory(context, org, seeded.planted);
  const definition = await storedMemory(context, org, seeded.definition);
  const decoy = await storedMemory(context, org, seeded.decoy);
  const conflicts = await findConflictingWorkMemories(
    { orgId: org.orgId, text: PLANTED_SLA.text, kind: PLANTED_SLA.kind, scope: "global" },
    0.9,
  );
  return {
    plantedStored: planted?.text === PLANTED_SLA.text,
    definitionStored: definition?.text === PLANTED_DEFINITION.text,
    decoyStored: decoy?.text === DECOY.text,
    scopeNormalized: planted?.scope === "global" && planted?.scopeId === null,
    dedupeDetected: conflicts.length > 0 && conflicts[0]!.memory.id === seeded.planted,
  };
}

async function searchPhase(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const seeded = await seed(context, org);
  const [hit, miss] = (await searchTurn(context, org, [SLA_QUERY, MISS_QUERY])) as [MemoryHit[], MemoryHit[]];
  const planted = await storedMemory(context, org, seeded.planted);
  return searchOutputFrom({ seeded, hit, miss, plantedUseCount: planted?.useCount as number | undefined });
}

async function isolatePhase(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const seeded = await seed(context, org);
  const stranger = await provisionOrg(context, `${org.orgId}b`);
  const [crossOrg] = (await searchTurn(context, stranger, [SLA_QUERY])) as [MemoryHit[]];
  const crossOrgRead = await apiJson(context, stranger, `/api/v1/work/memories/${seeded.planted}`);
  const crossOrgList = await apiJson(context, stranger, "/api/v1/work/memories");
  const [beforeArchive] = (await searchTurn(context, org, [DECOY_QUERY])) as [MemoryHit[]];
  const archived = await apiJson(context, org, `/api/v1/work/memories/${seeded.decoy}`, {
    method: "DELETE",
    body: JSON.stringify({ reason: "eval isolation phase" }),
  });
  const [afterArchive] = (await searchTurn(context, org, [DECOY_QUERY])) as [MemoryHit[]];
  return {
    crossOrgHidden:
      crossOrg.length === 0 &&
      crossOrgRead.status === 404 &&
      Array.isArray(crossOrgList.body.memories) &&
      crossOrgList.body.memories.length === 0,
    archiveAccepted: archived.status === 200 && archived.body.ok === true,
    archivedExcluded: texts(beforeArchive).includes(DECOY.text) && !texts(afterArchive).includes(DECOY.text),
  };
}

async function ablationWithMemory(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const seeded = await seed(context, org);
  const [sla, definition] = (await searchTurn(context, org, [SLA_QUERY, DEFINITION_QUERY])) as [MemoryHit[], MemoryHit[]];
  const recovered = /\b(\d{4,6}) USD\b/u.exec(sla[0]?.memory.text ?? "")?.[1];
  return {
    retrieved: sla.length > 0 && definition.length > 0,
    thresholdRecovered: recovered === "18500",
    definitionRecovered: definition[0]?.memory.id === seeded.definition,
  };
}

async function ablationWithoutMemory(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const ablated = await provisionOrg(context, `${org.orgId}c`);
  const [sla, definition] = (await searchTurn(context, ablated, [SLA_QUERY, DEFINITION_QUERY])) as [
    MemoryHit[],
    MemoryHit[],
  ];
  return {
    nothingRetrieved: sla.length === 0 && definition.length === 0,
    honestMiss: savedOnly(sla, definition),
  };
}

const SKILL_DRAFT = {
  name: "Revenue Variance Report",
  description: "Build a quarterly revenue variance report grounded in the connected source.",
  body: "Use the read-only data path; cite every figure.",
  allowedTools: "Bash(python3:*)",
  metadata: { audience: "finance" },
};
const UPDATED_DESCRIPTION = "Build a quarterly revenue variance report with citations.";

async function createSkill(context: WorkContext, org: WorkOrg, draft: Record<string, unknown>) {
  let result: Record<string, unknown> = {};
  await turn(context, org, "Save this as a skill.", async (model) => {
    const reply = await model.call("neko_skills", "create_skill", draft);
    result = { ...toolJson(reply), isError: reply.isError === true };
    return "Done.";
  });
  return result;
}

async function filesNamed(root: string, name: string): Promise<string[]> {
  const entries = await readdir(root, { recursive: true, withFileTypes: true }).catch(() => []);
  return entries.filter((entry) => entry.name === name).map((entry) => join(entry.parentPath, entry.name));
}

async function skillRoundTrip(context: WorkContext, org: WorkOrg): Promise<Record<string, unknown>> {
  const written = await createSkill(context, org, SKILL_DRAFT);
  const name = String(written.name ?? "");
  const first = await apiJson(context, org, `/api/v1/work/skills/${name}`);
  const firstSkill = (first.body.skill ?? {}) as Record<string, unknown>;
  const frontmatter = parseSkillFrontmatter(String(firstSkill.skillMarkdown ?? ""));
  const updated = await createSkill(context, org, { ...SKILL_DRAFT, description: UPDATED_DESCRIPTION });
  const second = await apiJson(context, org, `/api/v1/work/skills/${name}`);
  const secondSkill = (second.body.skill ?? {}) as Record<string, unknown>;
  const listed = await apiJson(context, org, "/api/v1/work/skills");
  const named = ((listed.body.skills ?? []) as Array<{ name?: string }>).filter((skill) => skill.name === name);
  const escape = await createSkill(context, org, {
    ...SKILL_DRAFT,
    files: [{ path: "../escape.txt", content: "must never be written" }],
  });
  const badName = await createSkill(context, org, { ...SKILL_DRAFT, name: "!!!" });
  const deps = aggregateSkillDeps();
  return {
    nameNormalized: name === "revenue-variance-report",
    frontmatterRoundTrip:
      first.status === 200 &&
      firstSkill.name === "revenue-variance-report" &&
      firstSkill.description === SKILL_DRAFT.description &&
      frontmatter.name === "revenue-variance-report",
    allowedToolsParsed: frontmatter.allowedTools === SKILL_DRAFT.allowedTools,
    upsertStable:
      updated.name === name &&
      secondSkill.path === firstSkill.path &&
      secondSkill.description === UPDATED_DESCRIPTION &&
      named.length === 1,
    escapeBlocked: escape.isError === true && (await filesNamed(context.episode.root, "escape.txt")).length === 0,
    badNameRejected: badName.isError === true && !existsSync(join(String(firstSkill.path ?? ""), "..", "!!!")),
    depsAggregated:
      Boolean(KNOWN_SKILL_DEPS["skill-creator"]) &&
      KNOWN_SKILL_DEPS["skill-creator"]!.pip.every((pkg) => deps.pip.includes(pkg)),
    depsDeduped: new Set(deps.pip).size === deps.pip.length,
  };
}

const PHASES: Record<string, Record<string, (context: WorkContext, org: WorkOrg) => Promise<Record<string, unknown>>>> = {
  "memory-retrieval": { seed: seedPhase, search: searchPhase, isolate: isolatePhase },
  "memory-ablation": { "with-memory": ablationWithMemory, "without-memory": ablationWithoutMemory },
  "skill-roundtrip": { initial: skillRoundTrip },
};

function expectedFor(evalCase: LoadedCase, phase: string): Record<string, unknown> {
  const spec = oracleSpecForPhase(evalCase, phase);
  if (spec?.kind !== "inline.expected") {
    throw new EvalEnvironmentError(
      `${evalCase.id} phase ${phase} requires an inline.expected oracle`,
      "oracle_invalid",
    );
  }
  return (spec.params?.expected as Record<string, unknown>) ?? {};
}

export function createOpenNekoWorkDriver(
  _context: { loaded: LoadedEval; plan: EvalPlan },
  options: { host?: EvalInstallHostOptions } = {},
): EvalDriver {
  let host: EvalInstallHost | undefined;
  let model: ScriptedModelServer | undefined;
  return {
    id: "openneko.work-memory",
    scorer: SCORER,
    async preflight({ loaded, plan }) {
      if (!(await dbReachable())) {
        throw new EvalEnvironmentError(
          "OpenNeko PostgreSQL is unreachable; work-memory evals require the migrated control-plane database",
          "openneko_database_unreachable",
        );
      }
      if (plan.slots.some((slot) => slot.variant.data_path !== "none")) {
        throw new EvalEnvironmentError(
          "openneko.work-memory requires data_path: none",
          "variant_incompatible",
        );
      }
      if (loaded.config.defaults.concurrency !== 1) {
        throw new EvalEnvironmentError(
          "openneko.work-memory requires concurrency: 1, because one scripted model serves every turn",
          "unsafe_concurrency",
        );
      }
      const allowedEffects = new Set(["isolated-test-org", "temp-skills-root"]);
      for (const evalCase of loaded.cases) {
        if (
          evalCase.family === "mutation" &&
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
                   and table_name in ('work_memory', 'work_pending_memory')) as schema_digest
      `);
      host ??= await EvalInstallHost.prepare(options.host);
      model ??= await ScriptedModelServer.start();
      return {
        datasetFingerprint: {
          dataset: loaded.cases[0]?.dataset,
          snapshot: loaded.datasetSnapshots.get(loaded.cases[0]?.dataset ?? ""),
          corpusDigest: shortDigest(
            [PLANTED_SLA.text, PLANTED_DEFINITION.text, DECOY.text].join("\n"),
          ),
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
      return Object.fromEntries(
        evalCase.phases.map((phase) => [phase, expectedFor(evalCase, phase)]),
      );
    },
    async reset() {},
    async execute({ runId, slot, observer, signal }) {
      if (signal.aborted) throw signal.reason;
      if (!host || !model) {
        throw new EvalEnvironmentError("the eval install is not prepared", "backend_runtime_missing");
      }
      const scenario = String(slot.case.input.scenario ?? "");
      const run = PHASES[scenario]?.[slot.phase];
      if (!run) throw new Error(`unsupported work-memory scenario ${scenario} phase ${slot.phase}`);
      const operationId = `work-memory:${shortDigest(slot.key)}`;
      const started = Date.now();
      await observer.observe({
        kind: "run.start",
        operationId,
        attributes: {
          "openneko.run.kind": "eval",
          "openneko.product.path": slot.case.product_path,
          "openneko.eval.case.id": slot.caseId,
          "openneko.eval.variant.id": slot.variantId,
          "openneko.eval.phase": slot.phase,
        },
      });
      const memoryStage = scenario === "memory-retrieval" && slot.phase === "search";
      const episode = await host.startEpisode({ label: `${slot.caseId}-${slot.phase}` });
      const turns = { count: 0, requests: 0 };
      try {
        episode.activate();
        const context: WorkContext = {
          episode,
          model,
          orgId: `eval-work-${shortDigest(`${runId}/${slot.variantId}/${slot.caseId}/${slot.repetition}`).slice(0, 16)}`,
          turns,
        };
        const org = await provisionOrg(context, context.orgId);
        if (memoryStage) {
          await observer.observe({ kind: "memory.start", operationId: `${operationId}:memory`, parentOperationId: operationId });
        }
        const output = await run(context, org);
        if (memoryStage) {
          await observer.observe({
            kind: "memory.end",
            operationId: `${operationId}:memory`,
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
    score({ case: evalCase, oracle, execution, phase }) {
      const output = execution.output;
      const bundle = (oracle ?? {}) as Record<string, Record<string, unknown>>;
      const expected = bundle[phase] ?? {};
      const checks = assertionsForPhase(evalCase.assertions, phase).map(
        (assertion) => {
          const path = String(assertion.params?.path ?? "");
          const actual =
            output && typeof output === "object"
              ? (output as Record<string, unknown>)[path]
              : undefined;
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
              ? `phase=${phase} path=${path}`
              : `phase=${phase} path=${path} expected=${JSON.stringify(wanted)} actual=${JSON.stringify(actual)}`,
          };
        },
      );
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
      host = undefined;
      model = undefined;
      await pool().end();
    },
  };
}
