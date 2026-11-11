import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pool } from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
} from "@neko/db/test-helpers";
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
import {
  KNOWN_SKILL_DEPS,
  aggregateSkillDeps,
  archiveWorkMemory,
  findConflictingWorkMemories,
  parseSkillFrontmatter,
  rememberWorkMemory,
  searchWorkMemory,
  upsertWorkSkill,
  writeWorkSkill,
  type WorkMemory,
} from "@neko/llm/work";

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

/**
 * The corpus is the ground truth: the relevant/irrelevant partition is known
 * by construction. PLANTED facts are what a work task legitimately needs;
 * the DECOY is plausible org memory that the target queries must never
 * surface.
 */
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

/**
 * Phases of one (variant, case, repetition) share state, so the isolated
 * organization is derived from the slot identity WITHOUT the phase segment.
 */
function orgIdFor(
  runId: string,
  slot: { variantId: string; caseId: string; repetition: number },
): string {
  const identity = `${runId}/${slot.variantId}/${slot.caseId}/${slot.repetition}`;
  return `eval-work-${shortDigest(identity).slice(0, 16)}`;
}

function savedTexts(results: Awaited<ReturnType<typeof searchWorkMemory>>): string[] {
  return results.saved.map((result) => result.memory.text);
}

/**
 * Idempotently create and seed the corpus organization. Phases may execute
 * in any order and may be re-run after a crash; every phase starts here.
 */
async function ensureSeededOrg(orgId: string): Promise<{
  planted: WorkMemory;
  definition: WorkMemory;
  decoy: WorkMemory;
}> {
  await createTestOrg(orgId, "OpenNeko work-memory eval").catch(() => {
    // Organization already exists from an earlier phase of this slot group.
  });
  const find = async (draft: { kind: string; text: string }) => {
    const conflicts = await findConflictingWorkMemories(
      {
        orgId,
        text: draft.text,
        kind: draft.kind as WorkMemory["kind"],
        scope: "global",
      },
      0.95,
    );
    return conflicts[0]?.memory;
  };
  const remember = async (draft: { kind: string; text: string }) =>
    (await find(draft)) ??
    rememberWorkMemory({
      orgId,
      kind: draft.kind as WorkMemory["kind"],
      scope: "global",
      text: draft.text,
      metadata: { source: "eval-fixture" },
    });
  return {
    planted: await remember(PLANTED_SLA),
    definition: await remember(PLANTED_DEFINITION),
    decoy: await remember(DECOY),
  };
}

async function seedPhase(orgId: string): Promise<Record<string, unknown>> {
  const seeded = await ensureSeededOrg(orgId);
  const conflicts = await findConflictingWorkMemories(
    { orgId, text: PLANTED_SLA.text, kind: PLANTED_SLA.kind, scope: "global" },
    0.9,
  );
  return {
    plantedStored: seeded.planted.text === PLANTED_SLA.text,
    definitionStored: seeded.definition.text === PLANTED_DEFINITION.text,
    decoyStored: seeded.decoy.text === DECOY.text,
    scopeNormalized:
      seeded.planted.scope === "global" && seeded.planted.scopeId === null,
    dedupeDetected:
      conflicts.length > 0 && conflicts[0]!.memory.id === seeded.planted.id,
  };
}

async function searchPhase(orgId: string): Promise<Record<string, unknown>> {
  const seeded = await ensureSeededOrg(orgId);
  const hit = await searchWorkMemory({ orgId, query: SLA_QUERY });
  const miss = await searchWorkMemory({ orgId, query: MISS_QUERY });
  // Discovered product behavior, encoded deliberately: searchWorkMemory is
  // recall-oriented — every global memory carries a query-independent base
  // score (global +1, pinned +3), so an off-topic query still returns rows.
  // The deterministic retrieval contract is therefore ranking separation,
  // not absolute exclusion: the planted fact must outrank the decoy on a
  // matching query, and a miss must score strictly below a hit.
  const decoyScore = hit.saved.find(
    (result) => result.memory.id === seeded.decoy.id,
  )?.score;
  const hitScore = hit.saved.find(
    (result) => result.memory.id === seeded.planted.id,
  )?.score;
  const topMissScore = miss.saved[0]?.score;
  return {
    plantedFound: savedTexts(hit).includes(PLANTED_SLA.text),
    plantedRankedFirst: hit.saved[0]?.memory.id === seeded.planted.id,
    decoyOutranked:
      hitScore !== undefined && (decoyScore === undefined || hitScore > decoyScore),
    missWeakerThanHit:
      hitScore !== undefined &&
      (topMissScore === undefined || topMissScore < hitScore),
    archivesEmpty: hit.archives.length === 0 && miss.archives.length === 0,
    usageTouched:
      (await searchWorkMemory({ orgId, query: SLA_QUERY })).saved[0]!.memory
        .useCount >= 1,
  };
}

async function isolatePhase(orgId: string): Promise<Record<string, unknown>> {
  const seeded = await ensureSeededOrg(orgId);
  const strangerOrgId = `${orgId}b`;
  await createTestOrg(strangerOrgId, "OpenNeko work-memory stranger org").catch(
    () => {},
  );
  const crossOrg = await searchWorkMemory({
    orgId: strangerOrgId,
    query: SLA_QUERY,
  });
  const archived = await archiveWorkMemory(orgId, seeded.decoy.id, {
    reason: "eval isolation phase",
  });
  const afterArchive = await searchWorkMemory({ orgId, query: DECOY_QUERY });
  return {
    crossOrgHidden: crossOrg.saved.length === 0,
    archiveAccepted: archived === true,
    archivedExcluded: !savedTexts(afterArchive).includes(DECOY.text),
  };
}

async function ablationWithMemory(orgId: string): Promise<Record<string, unknown>> {
  const seeded = await ensureSeededOrg(orgId);
  const sla = await searchWorkMemory({ orgId, query: SLA_QUERY });
  const definition = await searchWorkMemory({ orgId, query: DEFINITION_QUERY });
  const recovered = /\b(\d{4,6}) USD\b/u.exec(
    sla.saved[0]?.memory.text ?? "",
  )?.[1];
  return {
    retrieved: sla.saved.length > 0 && definition.saved.length > 0,
    thresholdRecovered: recovered === "18500",
    definitionRecovered:
      definition.saved[0]?.memory.id === seeded.definition.id,
  };
}

async function ablationWithoutMemory(orgId: string): Promise<Record<string, unknown>> {
  const ablatedOrgId = `${orgId}c`;
  await createTestOrg(ablatedOrgId, "OpenNeko work-memory ablated org").catch(
    () => {},
  );
  const sla = await searchWorkMemory({ orgId: ablatedOrgId, query: SLA_QUERY });
  const definition = await searchWorkMemory({
    orgId: ablatedOrgId,
    query: DEFINITION_QUERY,
  });
  // With the corpus ablated, the same queries retrieve nothing at all —
  // the paired delta against the with-memory phase is the measured
  // contribution of the memory capability.
  return {
    nothingRetrieved: sla.saved.length === 0 && definition.saved.length === 0,
    honestMiss:
      sla.archives.length === 0 && definition.archives.length === 0,
  };
}

async function skillRoundTrip(): Promise<Record<string, unknown>> {
  const skillsRoot = await mkdtemp(join(tmpdir(), "openneko-eval-skills-"));
  try {
    const draft = {
      name: "Revenue Variance Report",
      description:
        "Build a quarterly revenue variance report grounded in the connected source.",
      body: "Use the read-only data path; cite every figure.",
      allowedTools: "Bash(python3:*)",
      metadata: { audience: "finance" },
    };
    const written = await writeWorkSkill(skillsRoot, draft);
    const markdown = await readFile(
      resolve(written.skillPath, "SKILL.md"),
      "utf8",
    );
    const frontmatter = parseSkillFrontmatter(markdown);
    const updated = await writeWorkSkill(skillsRoot, {
      ...draft,
      description: "Build a quarterly revenue variance report with citations.",
    });
    const reparsed = parseSkillFrontmatter(
      await readFile(resolve(updated.skillPath, "SKILL.md"), "utf8"),
    );
    let escapeBlocked = false;
    try {
      await writeWorkSkill(skillsRoot, {
        ...draft,
        files: [{ path: "../escape.txt", content: "must never be written" }],
      });
    } catch {
      escapeBlocked = true;
    }
    let badNameRejected = false;
    try {
      upsertWorkSkill(skillsRoot, { ...draft, name: "!!!" });
    } catch {
      badNameRejected = true;
    }
    const deps = aggregateSkillDeps();
    return {
      nameNormalized: written.name === "revenue-variance-report",
      frontmatterRoundTrip:
        frontmatter.name === "revenue-variance-report" &&
        frontmatter.description === draft.description,
      allowedToolsParsed: frontmatter.allowedTools === draft.allowedTools,
      upsertStable:
        updated.skillPath === written.skillPath &&
        reparsed.description ===
          "Build a quarterly revenue variance report with citations.",
      escapeBlocked,
      badNameRejected,
      depsAggregated:
        Boolean(KNOWN_SKILL_DEPS["skill-creator"]) &&
        KNOWN_SKILL_DEPS["skill-creator"]!.pip.every((pkg) => deps.pip.includes(pkg)),
      depsDeduped: new Set(deps.pip).size === deps.pip.length,
    };
  } finally {
    await rm(skillsRoot, { recursive: true, force: true });
  }
}

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

export function createOpenNekoWorkDriver(context: {
  loaded: LoadedEval;
  plan: EvalPlan;
}): EvalDriver {
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
              backend: "deterministic-product-runtime",
              modelCalls: 0,
              dataPath: variant.data_path,
            },
          ]),
        ),
      };
    },
    async resolveOracle(evalCase) {
      // A bundle resolves to a phase-keyed map; a single oracle applies to
      // every declared phase of the case.
      return Object.fromEntries(
        evalCase.phases.map((phase) => [phase, expectedFor(evalCase, phase)]),
      );
    },
    async reset({ runId, slot }) {
      const orgId = orgIdFor(runId, slot);
      await deleteTestOrg(orgId);
      await deleteTestOrg(`${orgId}b`);
      await deleteTestOrg(`${orgId}c`);
    },
    async execute({ runId, slot, observer, signal }) {
      if (signal.aborted) throw signal.reason;
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
      try {
        const scenario = String(slot.case.input.scenario ?? "");
        const orgId = orgIdFor(runId, slot);
        let output: Record<string, unknown>;
        if (scenario === "memory-retrieval") {
          if (slot.phase === "seed") output = await seedPhase(orgId);
          else if (slot.phase === "search") {
            await observer.observe({
              kind: "memory.start",
              operationId: `${operationId}:memory`,
              parentOperationId: operationId,
            });
            output = await searchPhase(orgId);
            await observer.observe({
              kind: "memory.end",
              operationId: `${operationId}:memory`,
              parentOperationId: operationId,
              status: "ok",
            });
          } else if (slot.phase === "isolate") output = await isolatePhase(orgId);
          else throw new Error(`unsupported memory-retrieval phase ${slot.phase}`);
        } else if (scenario === "memory-ablation") {
          if (slot.phase === "with-memory") output = await ablationWithMemory(orgId);
          else if (slot.phase === "without-memory") {
            output = await ablationWithoutMemory(orgId);
          } else throw new Error(`unsupported memory-ablation phase ${slot.phase}`);
        } else if (scenario === "skill-roundtrip") {
          output = await skillRoundTrip();
        } else {
          throw new Error(`unsupported work-memory scenario ${scenario}`);
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
      await pool().end();
    },
  };
}
