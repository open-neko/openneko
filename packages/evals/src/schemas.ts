import { z } from "zod";

const Id = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-z0-9][a-z0-9._-]*$/u, "must be a stable lowercase identifier");
const Version = z.string().min(1).max(64);
const EnvRef = z.string().regex(/^env:[A-Z][A-Z0-9_]*$/u);
const Duration = z.string().regex(/^\d+(?:ms|s|m|h)$/u);
const Ref = z.object({ ref: z.string().min(1) }).strict();
const SemanticId = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[A-Z0-9][A-Z0-9._-]*$/u, "must be a stable semantic ID");
const ScoreDimension = z.enum([
  "ground_truth",
  "method",
  "behavior",
  "safety",
  "efficiency",
]);

export const PricingCatalogSchema = z
  .object({
    schema_version: z.literal("openneko.eval.pricing/v1"),
    id: Id,
    version: Version,
    currency: z.literal("USD"),
    effective_date: z.string().date(),
    sources: z.array(z.string().url()).min(1),
    entries: z
      .array(
        z
          .object({
            provider: z.string().min(1),
            model: z.string().min(1),
            input_usd_per_million_tokens: z.number().nonnegative(),
            output_usd_per_million_tokens: z.number().nonnegative(),
            cache_read_usd_per_million_tokens: z.number().nonnegative().optional(),
            cache_write_usd_per_million_tokens: z.number().nonnegative().optional(),
            notes: z.string().max(1024).optional(),
          })
          .strict(),
      )
      .min(1),
  })
  .strict();

export const ModelConfigSchema = z
  .object({
    provider: z.string().min(1),
    model: z.string().min(1),
    credential_ref: EnvRef.optional(),
    config: z.record(z.string(), z.unknown()).optional(),
  })
  .strict();

export const VariantSchema = z
  .object({
    id: Id,
    extends: Id.optional(),
    backend: Id,
    outer_model: ModelConfigSchema,
    data_path: z.enum([
      "graphjin-direct",
      "graphjin-agent",
      "planner-host-execute",
      "none",
    ]),
    inner_model: ModelConfigSchema.optional(),
    settings: z.record(z.string(), z.unknown()).optional(),
  })
  .strict();

export const EvalConfigSchema = z
  .object({
    schema_version: z.literal("openneko.eval/v1"),
    id: Id,
    description: z.string().max(2048).optional(),
    adapter: Id,
    semantics: Ref.optional(),
    pricing: Ref.optional(),
    suite: Ref.extend({ cases: z.array(Id).min(1).optional() }).strict(),
    datasets: z
      .array(
        Ref.extend({
          snapshot: Id,
          settings: z.record(z.string(), z.unknown()).optional(),
        }).strict(),
      )
      .min(1),
    defaults: z
      .object({
        repetitions: z.number().int().min(1).max(100).default(1),
        timeout: Duration.default("10m"),
        execution_order: z
          .enum(["declared", "randomized", "counterbalanced"])
          .default("counterbalanced"),
        concurrency: z.number().int().min(1).max(100).default(1),
        cache_state: z.enum(["cold", "warm", "mixed"]).default("warm"),
        content_capture: z.enum(["off", "metadata", "redacted"]).default("metadata"),
        max_attempts: z.number().int().min(1).max(10).default(2),
      })
      .strict()
      .default({
        repetitions: 1,
        timeout: "10m",
        execution_order: "counterbalanced",
        concurrency: 1,
        cache_state: "warm",
        content_capture: "metadata",
        max_attempts: 2,
      }),
    variants: z.array(VariantSchema).min(1),
    budgets: z
      .object({
        max_wall_time: Duration.optional(),
        max_estimated_cost_usd: z.number().nonnegative().optional(),
      })
      .strict()
      .optional(),
    artifacts: z
      .object({
        check_in: z.enum(["none", "scored"]).default("scored"),
        raw_dir: z.string().min(1).default(".openneko/evals/raw"),
        state_dir: z.string().min(1).default(".openneko/evals/state"),
        results_dir: z.string().min(1).default("evals/results"),
      })
      .strict()
      .default({
        check_in: "scored",
        raw_dir: ".openneko/evals/raw",
        state_dir: ".openneko/evals/state",
        results_dir: "evals/results",
      }),
  })
  .strict();

export const DatasetSchema = z
  .object({
    schema_version: z.literal("openneko.eval.dataset/v1"),
    id: Id,
    version: Version,
    description: z.string().max(4096).optional(),
    license: z.string().min(1),
    capabilities: z.array(Id).min(1),
    snapshots: z
      .array(
        z
          .object({
            id: Id,
            description: z.string().optional(),
            seed: z.number().int().optional(),
          })
          .strict(),
      )
      .min(1),
    connection: z
      .object({
        agent_graphql_ref: EnvRef.optional(),
        agent_mcp_ref: EnvRef.optional(),
        oracle_database_ref: EnvRef.optional(),
        defaults: z.record(z.string(), z.string()).optional(),
      })
      .strict(),
    anchor_policy: z
      .object({
        kind: z.enum(["fixed", "data-derived"]),
        value: z.string().optional(),
        description: z.string().optional(),
      })
      .strict(),
  })
  .strict();

export const AssertionSchema = z
  .object({
    id: Id,
    dimension: ScoreDimension,
    kind: Id,
    gate: z.boolean().default(false),
    // V4 capability attribution is assertion-level. These remain optional so
    // published v1-v3 case documents retain their exact content identities.
    capabilities: z.array(Id).min(1).optional(),
    semantics: z.array(SemanticId).min(1).optional(),
    // Binds the assertion to one phase of a multi-phase case. Absent =
    // the assertion applies to every phase.
    phase: Id.optional(),
    params: z.record(z.string(), z.unknown()).optional(),
  })
  .strict();

export const OracleSpecSchema = z
  .object({
    kind: Id,
    ref: z.string().min(1).optional(),
    params: z.record(z.string(), z.unknown()).optional(),
  })
  .strict();

export const CaseComparisonSchema = z
  .object({
    pair_id: Id,
    treatment: Id,
  })
  .strict();

export const MetricQuestionInputSchema = z
  .object({
    question: z.string().trim().min(1).max(4096),
    role: z.enum(["CEO", "CFO", "COO", "CRO", "CMO", "CIO", "CPO"]),
  })
  .strict();

export const CaseSchema = z
  .object({
    schema_version: z.literal("openneko.eval.case/v1"),
    id: Id,
    version: Version,
    family: z.enum(["read", "watcher", "mutation", "resilience", "contract"]),
    product_path: Id,
    dataset: Id,
    capability_tags: z.array(Id).min(1),
    difficulty: Id,
    semantics: z.array(SemanticId).min(1),
    input: z.record(z.string(), z.unknown()),
    comparison: CaseComparisonSchema.optional(),
    // Exactly one of `oracle` (whole-episode ground truth) or `oracles`
    // (a phase-keyed bundle for multi-phase cases) must be present.
    oracle: OracleSpecSchema.optional(),
    oracles: z.record(Id, OracleSpecSchema).optional(),
    phases: z.array(Id).min(1).default(["initial"]),
    timeout: Duration.optional(),
    allowed_side_effects: z.array(Id).default([]),
    assertions: z.array(AssertionSchema).min(1),
  })
  .strict()
  .superRefine((value, context) => {
    if (Boolean(value.oracle) === Boolean(value.oracles)) {
      context.addIssue({
        code: "custom",
        path: ["oracle"],
        message: "declare exactly one of oracle or oracles",
      });
    }
    const phases = new Set(value.phases);
    for (const phase of Object.keys(value.oracles ?? {})) {
      if (!phases.has(phase)) {
        context.addIssue({
          code: "custom",
          path: ["oracles", phase],
          message: `oracle phase ${phase} is not in phases`,
        });
      }
    }
    for (const [index, assertion] of value.assertions.entries()) {
      if (assertion.phase && !phases.has(assertion.phase)) {
        context.addIssue({
          code: "custom",
          path: ["assertions", index, "phase"],
          message: `assertion phase ${assertion.phase} is not in phases`,
        });
      }
    }
  });

export const ThresholdGateSchema = z
  .object({
    id: Id,
    qualification: z.enum(["capability", "reliability", "safety"]),
    metric: z.enum([
      "macro-ground-truth",
      "macro-method",
      "macro-behavior",
      "full-task-pass-rate",
      "episode-completion-rate",
      "token-usage-coverage",
      "capability-unconditional-pass-rate",
      "capability-conditional-pass-rate",
      "capability-coverage",
      "safety-assertion-failures",
      "security-outcome-count",
    ]),
    capability: Id.optional(),
    security_outcome: z
      .enum(["assertion_failed", "attempted", "blocked", "completed"])
      .optional(),
    severity_at_least: z
      .enum(["low", "medium", "high", "critical"])
      .optional(),
    operator: z.enum(["gte", "lte", "eq"]),
    value: z.number().nonnegative(),
    minimum_samples: z.number().int().positive().optional(),
    minimum_coverage: z.number().min(0).max(1).optional(),
    enforcement: z.enum(["required", "advisory"]),
    severity: z.enum(["low", "medium", "high", "critical"]),
    owner: z.string().min(1).max(256),
    rationale: z.string().min(1).max(2048),
    calibration_runs: z.array(Id),
  })
  .strict()
  .superRefine((value, context) => {
    const capabilityMetric = value.metric.startsWith("capability-");
    if (capabilityMetric !== Boolean(value.capability)) {
      context.addIssue({
        code: "custom",
        path: ["capability"],
        message: capabilityMetric
          ? "capability metrics require a capability selector"
          : "capability selector is only valid for capability metrics",
      });
    }
    if (
      value.metric === "security-outcome-count" &&
      !value.security_outcome
    ) {
      context.addIssue({
        code: "custom",
        path: ["security_outcome"],
        message: "security-outcome-count requires an outcome selector",
      });
    }
    if (
      value.metric !== "security-outcome-count" &&
      (value.security_outcome || value.severity_at_least)
    ) {
      context.addIssue({
        code: "custom",
        path: ["security_outcome"],
        message: "security selectors are only valid for security-outcome-count",
      });
    }
  });

export const ThresholdPolicySchema = z
  .object({
    schema_version: z.literal("openneko.eval.threshold-policy/v1"),
    id: Id,
    version: Version,
    status: z.enum(["provisional", "calibrated"]),
    owner: z.string().min(1).max(256),
    introduced: z.string().date(),
    last_reviewed: z.string().date(),
    description: z.string().min(1).max(4096),
    capabilities: z
      .array(
        z
          .object({
            id: Id,
            display_name: z.string().min(1).max(128),
            group: Id,
            description: z.string().min(1).max(1024),
          })
          .strict(),
      )
      .default([]),
    gates: z.array(ThresholdGateSchema).min(1),
    history: z
      .array(
        z
          .object({
            date: z.string().date(),
            version: Version,
            change: z.string().min(1).max(2048),
          })
          .strict(),
      )
      .min(1),
  })
  .strict();

export const SuiteSchema = z
  .object({
    schema_version: z.literal("openneko.eval.suite/v1"),
    id: Id,
    version: Version,
    description: z.string().max(4096).optional(),
    threshold_policy: Ref.optional(),
    cases: z
      .array(
        Ref.extend({
          assertion_attribution: z
            .record(
              Id,
              z
                .object({
                  capabilities: z.array(Id).min(1),
                  semantics: z.array(SemanticId).min(1).optional(),
                })
                .strict(),
            )
            .optional(),
        }).strict(),
      )
      .min(1),
    gates: z
      .object({
        min_macro_ground_truth: z.number().min(0).max(1).optional(),
        min_method: z.number().min(0).max(1).optional(),
        min_behavior: z.number().min(0).max(1).optional(),
        min_full_task_pass_rate: z.number().min(0).max(1).optional(),
        min_token_usage_coverage: z.number().min(0).max(1).optional(),
        min_capability_task_pass_rate: z
          .record(Id, z.number().min(0).max(1))
          .optional(),
        max_unsafe_effects: z.number().int().nonnegative().optional(),
        require_safety: z.boolean().default(true),
      })
      .strict()
      .default({ require_safety: true }),
  })
  .strict();

export const SemanticRegistrySchema = z
  .object({
    schema_version: z.literal("openneko.eval.semantics/v1"),
    generated_from: z.string().min(1),
    entries: z
      .array(
        z
          .object({
            id: SemanticId,
            category: Id,
            description: z.string().min(1),
            verification: z.string().min(1),
            disposition: z.enum(["declared", "eval", "non-eval"]),
            owners: z.array(z.string().min(1)).min(1),
          })
          .strict(),
      )
      .min(1),
  })
  .strict();

export const ScoreCheckSchema = z
  .object({
    assertionId: Id,
    dimension: ScoreDimension,
    passed: z.boolean(),
    score: z.number().min(0).max(1),
    gate: z.boolean(),
    capabilities: z.array(Id).min(1).optional(),
    semantics: z.array(SemanticId).min(1).optional(),
    diagnostic: z.string().max(1024).optional(),
  })
  .strict();

export const ScoreVectorSchema = z
  .object({
    groundTruth: z.number().min(0).max(1),
    method: z.number().min(0).max(1),
    behavior: z.number().min(0).max(1),
    safety: z.number().min(0).max(1),
    efficiency: z.number().min(0).max(1),
  })
  .strict();

export const ScoreCoverageSchema = z
  .object({
    groundTruth: z.boolean(),
    method: z.boolean(),
    behavior: z.boolean(),
    safety: z.boolean(),
    efficiency: z.boolean(),
  })
  .strict();

export const UnsafeEffectSchema = z
  .object({
    kind: Id,
    capability: Id,
    target: Id,
    assertionId: Id,
    source: z.enum(["trusted-broker", "trusted-host"]),
    operation: Id,
    sequence: z.number().int().positive(),
  })
  .strict();

export const SecurityOutcomeSchema = z
  .object({
    outcome: z.enum(["assertion_failed", "attempted", "blocked", "completed"]),
    kind: Id,
    severity: z.enum(["low", "medium", "high", "critical"]),
    capability: Id,
    semantic: SemanticId.optional(),
    assertionId: Id,
    actorRole: z.enum(["member", "service"]),
    organizationScope: z.enum(["same", "cross", "unknown"]),
    targetType: Id,
    target: Id.optional(),
    enforcementDecision: z.enum([
      "allowed",
      "blocked",
      "bypassed",
      "not-applicable",
    ]),
    stateBeforeDigest: z.string().startsWith("sha256:").optional(),
    stateAfterDigest: z.string().startsWith("sha256:").optional(),
    source: z.enum(["trusted-broker", "trusted-host"]),
    operation: Id,
    sequence: z.number().int().nonnegative(),
  })
  .strict();

export const AssertionTargetSchema = z
  .object({
    assertionId: Id,
    dimension: ScoreDimension,
    gate: z.boolean(),
    capabilities: z.array(Id).min(1),
    semantics: z.array(SemanticId).default([]),
  })
  .strict();

export const ScoreSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.score/v1"),
    scorerId: Id,
    scorerVersion: Version,
    scorerDigest: z.string().startsWith("sha256:"),
    verdict: z.enum(["pass", "fail"]),
    vector: ScoreVectorSchema,
    coverage: ScoreCoverageSchema,
    checks: z.array(ScoreCheckSchema),
    // Optional keeps score/v1 checkpoints written before explicit unsafe-effect
    // classification parseable without changing their integrity digests.
    unsafeEffects: z.array(UnsafeEffectSchema).optional(),
    // V4 outcomes distinguish evidence gaps, blocked attempts, and completed
    // prohibited effects. Legacy unsafeEffects remains for v1-v3 verification.
    securityOutcomes: z.array(SecurityOutcomeSchema).optional(),
  })
  .strict();

export const AttemptSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.attempt/v1"),
    runId: Id,
    slotKey: z.string().min(1),
    attempt: z.number().int().min(1),
    event: z.enum(["started", "finished"]),
    timestamp: z.string().datetime(),
    status: z.enum(["running", "completed", "failed", "environment_failure"]),
    errorType: z.string().max(128).optional(),
    error: z.string().max(2048).optional(),
  })
  .strict();

/**
 * Private, scorer-facing evidence captured at trusted product boundaries.
 *
 * Evidence belongs only in the resumable state store. Producers should retain
 * stable identifiers and digests instead of raw customer content; promoted
 * result artifacts omit the complete document regardless.
 */
export const EvalSemanticEvidenceEventSchema = z
  .object({
    schemaVersion: z.literal("openneko.work.semantic-trace/v1"),
    sequence: z.number().int().positive(),
    timestamp: z.string().datetime(),
    runId: z.string().min(1),
    orgId: z.string().min(1),
    runKind: z.enum(["work", "workflow", "agent-job"]),
    source: z.enum(["trusted-broker", "trusted-host"]),
    operation: Id,
    status: z.enum(["ok", "error"]),
    durationMs: z.number().nonnegative(),
    requestDigest: z.string().startsWith("sha256:"),
    responseDigest: z.string().startsWith("sha256:").optional(),
    errorType: z.string().min(1).max(128).optional(),
    errorDigest: z.string().startsWith("sha256:").optional(),
    evidence: z.record(z.string(), z.unknown()),
  })
  .strict();

export const EvalSemanticEvidenceSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.semantic-evidence/v1"),
    events: z.array(EvalSemanticEvidenceEventSchema),
  })
  .strict();

export const EpisodeSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.episode/v1"),
    runId: Id,
    slotKey: z.string().min(1),
    // Optional for compatibility with episode/v1 checkpoints written before
    // candidate-neutral comparison identities were introduced.
    pairKey: z.string().min(1).optional(),
    treatment: Id.optional(),
    caseId: Id,
    caseContentId: z.string().startsWith("sha256:"),
    family: z.enum(["read", "watcher", "mutation", "resilience", "contract"]),
    productPath: Id,
    difficulty: Id,
    capabilityTags: z.array(Id),
    semantics: z.array(z.string()),
    assertionTargets: z.array(AssertionTargetSchema).optional(),
    variantId: Id,
    datasetId: Id,
    repetition: z.number().int().min(1),
    phase: Id,
    attempt: z.number().int().min(1),
    startedAt: z.string().datetime(),
    finishedAt: z.string().datetime(),
    status: z.enum(["completed", "failed", "environment_failure"]),
    output: z.unknown().optional(),
    measurements: z.record(z.string(), z.unknown()).default({}),
    observations: z.array(z.unknown()).default([]),
    // Optional preserves the integrity digest of episodes written before
    // semantic evidence was introduced. Do not add a default here.
    semanticEvidence: EvalSemanticEvidenceSchema.optional(),
    score: ScoreSchema.optional(),
    errorType: z.string().max(128).optional(),
    error: z.string().max(2048).optional(),
    integrityDigest: z.string().startsWith("sha256:"),
  })
  .strict();

export const ManifestSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.manifest/v1"),
    runnerVersion: Version,
    runId: Id,
    configId: Id,
    suiteId: Id,
    attestation: z.enum(["self-reported", "ci-attested"]),
    suiteGates: SuiteSchema.shape.gates,
    thresholdPolicy: ThresholdPolicySchema.optional(),
    thresholdPolicyDigest: z.string().startsWith("sha256:").optional(),
    runtimeBudgets: z
      .object({
        perEpisodeTimeoutMs: z.number().int().positive(),
        providerOutputTokenLimit: z.number().int().positive().nullable(),
        modelContextTokenLimit: z.number().int().positive().nullable(),
        toolCallCeilings: z.record(Id, z.number().int().positive().nullable()),
        harnessMaxAttempts: z.number().int().positive(),
        backendRetryAttempts: z.record(Id, z.number().int().nonnegative()),
        concurrency: z.number().int().positive(),
        cacheState: z.enum(["cold", "warm", "mixed"]),
        configuredModels: z.record(
          Id,
          z
            .object({
              provider: z.string().min(1),
              model: z.string().min(1),
            })
            .strict(),
        ),
        resolutionNotes: z.array(z.string().min(1).max(512)),
      })
      .strict()
      .optional(),
    status: z.enum(["in_progress", "complete"]),
    createdAt: z.string().datetime(),
    updatedAt: z.string().datetime(),
    source: z.object({
      commit: z.string().min(1),
      dirty: z.boolean(),
    }),
    compatibility: z.object({
      configDigest: z.string().startsWith("sha256:"),
      suiteDigest: z.string().startsWith("sha256:"),
      datasetDigest: z.string().startsWith("sha256:"),
      oracleDigest: z.string().startsWith("sha256:"),
      scorerDigest: z.string().startsWith("sha256:"),
      variantDigest: z.string().startsWith("sha256:"),
      sourceDigest: z.string().startsWith("sha256:"),
      thresholdPolicyDigest: z.string().startsWith("sha256:").optional(),
    }),
    // Optional preserves old v1 checkpoints. New runs retain the sanitized
    // runtime identity used to derive compatibility.datasetDigest.
    datasetFingerprint: z.unknown().optional(),
    resolvedVariants: z.record(z.string(), z.unknown()).default({}),
    effectiveConfig: EvalConfigSchema,
    // Unlike expectedSlotKeys (a canonical sorted set), this preserves the
    // actual declared/counterbalanced execution sequence.
    plannedSlotKeys: z.array(z.string()).optional(),
    expectedSlotKeys: z.array(z.string()),
    completedSlotKeys: z.array(z.string()),
    failedSlotKeys: z.array(z.string()),
    inFlightSlotKeys: z.array(z.string()),
    resultDir: z.string().optional(),
  })
  .strict();

export const OracleJournalSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.oracles/v1"),
    digest: z.string().startsWith("sha256:"),
    values: z.record(z.string(), z.unknown()),
  })
  .strict();

export const UsageSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.usage/v1"),
    scope: z.enum(["outer", "inner", "total"]),
    inputTokens: z.number().int().nonnegative().optional(),
    outputTokens: z.number().int().nonnegative().optional(),
    cacheReadTokens: z.number().int().nonnegative().optional(),
    cacheWriteTokens: z.number().int().nonnegative().optional(),
    reasoningTokens: z.number().int().nonnegative().optional(),
    totalTokens: z.number().int().nonnegative().optional(),
    estimatedCostUsd: z.number().nonnegative().optional(),
    billedCostUsd: z.number().nonnegative().optional(),
    currency: z.string().length(3).optional(),
    pricingCatalogVersion: Version.optional(),
    coverage: z.enum(["complete", "partial", "unavailable"]),
    missingReasons: z.array(z.string().max(512)).optional(),
  })
  .strict();

export const GeneratorSchema = z
  .object({
    schema_version: z.literal("openneko.eval.generator/v1"),
    id: Id,
    version: Version,
    dataset: Id,
    seed: z.number().int(),
    count: z.number().int().positive(),
    quotas: z
      .array(
        z
          .object({
            capability: Id,
            difficulty: Id.optional(),
            minimum: z.number().int().nonnegative(),
            maximum: z.number().int().positive(),
          })
          .strict(),
      )
      .min(1),
    settings: z.record(z.string(), z.unknown()).default({}),
  })
  .strict();

const SummaryGroupSchema = z
  .object({
    tasks: z.number().int().nonnegative(),
    passed: z.number().int().nonnegative(),
    passRate: z.number().min(0).max(1),
    vector: ScoreVectorSchema,
  })
  .strict();

const MeasurementAggregateSchema = z
  .object({
    count: z.number().int().nonnegative(),
    coverage: z.number().min(0).max(1),
    total: z.number().nonnegative(),
    mean: z.number().nonnegative().nullable(),
    p50: z.number().nonnegative().nullable(),
    p95: z.number().nonnegative().nullable(),
    max: z.number().nonnegative().nullable(),
  })
  .strict();

const EmptyMeasurementAggregate = {
  count: 0,
  coverage: 0,
  total: 0,
  mean: null,
  p50: null,
  p95: null,
  max: null,
} as const;

const AssertionCapabilitySummarySchema = z
  .object({
    attemptedEpisodes: z.number().int().nonnegative(),
    completedEpisodes: z.number().int().nonnegative(),
    unavailableEpisodes: z.number().int().nonnegative(),
    attemptedAssertions: z.number().int().nonnegative(),
    passingAssertions: z.number().int().nonnegative(),
    failingAssertions: z.number().int().nonnegative(),
    unavailableAssertions: z.number().int().nonnegative(),
    unconditionalPassRate: z.number().min(0).max(1),
    conditionalPassRate: z.number().min(0).max(1).nullable(),
    coverage: z.number().min(0).max(1),
    unconditional95CI: z
      .tuple([z.number().min(0).max(1), z.number().min(0).max(1)])
      .nullable(),
    conditional95CI: z
      .tuple([z.number().min(0).max(1), z.number().min(0).max(1)])
      .nullable(),
  })
  .strict();

const SecurityOutcomeSummarySchema = z
  .object({
    total: z.number().int().nonnegative(),
    episodes: z.number().int().nonnegative(),
    byOutcome: z.record(z.string(), z.number().int().nonnegative()),
    byKind: z.record(z.string(), z.number().int().nonnegative()),
    bySeverity: z.record(z.string(), z.number().int().nonnegative()),
    byOutcomeSeverity: z.record(
      z.string(),
      z.number().int().nonnegative(),
    ),
    byOutcomeKind: z.record(z.string(), z.number().int().nonnegative()),
  })
  .strict();

export const GateResultSchema = z
  .object({
    id: Id,
    qualification: z.enum(["capability", "reliability", "safety"]),
    metric: z.string().min(1),
    capability: Id.optional(),
    operator: z.enum(["gte", "lte", "eq"]),
    required: z.number().nonnegative(),
    observed: z.number().nonnegative().nullable(),
    samples: z.number().int().nonnegative(),
    coverage: z.number().min(0).max(1).nullable(),
    enforcement: z.enum(["required", "advisory"]),
    severity: z.enum(["low", "medium", "high", "critical"]),
    owner: z.string().min(1).max(256),
    rationale: z.string().min(1).max(2048),
    calibrationRuns: z.array(Id),
    status: z.enum(["pass", "fail", "insufficient_evidence"]),
    explanation: z.string().min(1).max(1024),
  })
  .strict();

export const QualificationSchema = z
  .object({
    integrity: z.enum(["valid", "invalid"]),
    capabilityQualification: z.enum([
      "pass",
      "fail",
      "insufficient_evidence",
    ]),
    reliabilityQualification: z.enum([
      "pass",
      "fail",
      "insufficient_evidence",
    ]),
    safetyQualification: z.enum([
      "pass",
      "fail",
      "insufficient_evidence",
    ]),
    productionQualification: z.enum(["pass", "fail"]),
    gateResults: z.array(GateResultSchema),
  })
  .strict();

export const SummarySchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.summary/v1"),
    expectedEpisodes: z.number().int().nonnegative(),
    scoredEpisodes: z.number().int().nonnegative(),
    executionFailures: z.number().int().nonnegative(),
    taskCount: z.number().int().nonnegative(),
    passedTasks: z.number().int().nonnegative(),
    taskPassRate: z.number().min(0).max(1),
    meanConsistency: z.number().min(0).max(1),
    macro: ScoreVectorSchema,
    micro: ScoreVectorSchema,
    coverage: z.object({
      groundTruth: z.number().min(0).max(1),
      method: z.number().min(0).max(1),
      behavior: z.number().min(0).max(1),
      safety: z.number().min(0).max(1),
      efficiency: z.number().min(0).max(1),
    }),
    measurements: z
      .object({
        wallDurationMs: MeasurementAggregateSchema,
        firstOutputMs: MeasurementAggregateSchema,
        // Defaults preserve v1 result documents written before backend
        // tool-efficiency aggregation was introduced.
        toolCalls: MeasurementAggregateSchema.default(EmptyMeasurementAggregate),
        repeatedToolCalls: MeasurementAggregateSchema.default(
          EmptyMeasurementAggregate,
        ),
        maxToolCalls: MeasurementAggregateSchema.default(
          EmptyMeasurementAggregate,
        ),
        inputTokens: MeasurementAggregateSchema,
        outputTokens: MeasurementAggregateSchema,
        cacheReadTokens: MeasurementAggregateSchema,
        cacheWriteTokens: MeasurementAggregateSchema,
        reasoningTokens: MeasurementAggregateSchema,
        totalTokens: MeasurementAggregateSchema,
        estimatedCostUsd: MeasurementAggregateSchema,
        billedCostUsd: MeasurementAggregateSchema,
        usageCoverage: z
          .object({
            completeEpisodes: z.number().int().nonnegative(),
            partialEpisodes: z.number().int().nonnegative(),
            unavailableEpisodes: z.number().int().nonnegative(),
            completeRate: z.number().min(0).max(1),
            availableRate: z.number().min(0).max(1),
          })
          .strict(),
        costCoverage: z
          .object({
            completeEpisodes: z.number().int().nonnegative(),
            partialEpisodes: z.number().int().nonnegative(),
            unavailableEpisodes: z.number().int().nonnegative(),
            completeRate: z.number().min(0).max(1),
            availableRate: z.number().min(0).max(1),
          })
          .strict(),
      })
      .strict(),
    macroGroundTruth95CI: z.tuple([z.number().min(0).max(1), z.number().min(0).max(1)]),
    safetyGateFailures: z.number().int().nonnegative(),
    behaviorGateFailures: z.number().int().nonnegative(),
    unsafeEffects: z.number().int().nonnegative().default(0),
    unsafeEffectEpisodes: z.number().int().nonnegative().default(0),
    unsafeEffectsByKind: z
      .record(z.string(), z.number().int().nonnegative())
      .default({}),
    assertionCapabilities: z
      .record(Id, AssertionCapabilitySummarySchema)
      .default({}),
    securityOutcomes: SecurityOutcomeSummarySchema.default({
      total: 0,
      episodes: 0,
      byOutcome: {},
      byKind: {},
      bySeverity: {},
      byOutcomeSeverity: {},
      byOutcomeKind: {},
    }),
    qualification: QualificationSchema.optional(),
    tasks: z.array(
      z.object({
        key: z.string().min(1),
        variantId: Id,
        caseId: Id,
        datasetId: Id,
        phase: Id,
        family: z.enum(["read", "watcher", "mutation", "resilience", "contract"]),
        productPath: Id,
        difficulty: Id,
        capabilityTags: z.array(Id),
        semantics: z.array(z.string().min(1)),
        repetitions: z.number().int().positive(),
        passes: z.number().int().nonnegative(),
        majorityPass: z.boolean(),
        consistency: z.number().min(0).max(1),
        unsafeEffects: z.number().int().nonnegative().default(0),
        vector: ScoreVectorSchema,
        passAtK: z.record(z.string(), z.number().min(0).max(1).nullable()),
        passPowerK: z.record(z.string(), z.number().min(0).max(1).nullable()),
      }),
    ),
    byDifficulty: z.record(z.string(), SummaryGroupSchema),
    byFamily: z.record(z.string(), SummaryGroupSchema),
    byCapability: z.record(z.string(), SummaryGroupSchema),
    bySemantic: z.record(z.string(), SummaryGroupSchema),
    byDataset: z.record(z.string(), SummaryGroupSchema),
    byProductPath: z.record(z.string(), SummaryGroupSchema),
    byVariant: z.record(z.string(), SummaryGroupSchema),
    failureTypes: z.record(z.string(), z.number().int().nonnegative()),
    failureDetails: z
      .array(
        z
          .object({
            type: z.string().min(1).max(128),
            episodes: z.number().int().positive(),
            taskIds: z.array(Id),
          })
          .strict(),
      )
      .default([]),
  })
  .strict();

export const ResultLineSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.result/v1"),
    runId: Id,
    slotKey: z.string().min(1),
    // Optional so previously promoted result/v1 artifacts remain verifiable.
    pairKey: z.string().min(1).optional(),
    treatment: Id.optional(),
    caseId: Id,
    caseContentId: z.string().startsWith("sha256:"),
    family: z.enum(["read", "watcher", "mutation", "resilience", "contract"]),
    productPath: Id,
    difficulty: Id,
    capabilityTags: z.array(Id),
    semantics: z.array(z.string()),
    assertionTargets: z.array(AssertionTargetSchema).optional(),
    variantId: Id,
    datasetId: Id,
    repetition: z.number().int().positive(),
    phase: Id,
    attempt: z.number().int().positive(),
    startedAt: z.string().datetime(),
    finishedAt: z.string().datetime(),
    status: z.enum(["completed", "failed", "environment_failure"]),
    measurements: z.unknown(),
    score: ScoreSchema.optional(),
    errorType: z.string().max(128).optional(),
    error: z.string().max(2048).optional(),
    episodeDigest: z.string().startsWith("sha256:"),
  })
  .strict();

export const ResultManifestSchema = z
  .object({
    schemaVersion: z.literal("openneko.eval.result-manifest/v1"),
    runId: Id,
    configId: Id,
    suiteId: Id,
    attestation: z.enum(["self-reported", "ci-attested"]),
    // Optional preserves verification of result/v1 artifacts promoted before
    // acceptance was recorded explicitly.
    accepted: z.boolean().optional(),
    suiteGates: SuiteSchema.shape.gates,
    thresholdPolicy: ManifestSchema.shape.thresholdPolicy,
    thresholdPolicyDigest: ManifestSchema.shape.thresholdPolicyDigest,
    runtimeBudgets: ManifestSchema.shape.runtimeBudgets,
    sourceRunManifestDigest: z.string().startsWith("sha256:"),
    // Present when a completed run was deterministically re-scored and the
    // sanitized result was promoted without invoking the candidate again.
    // Optional preserves result-manifest/v1 artifacts promoted before this
    // provenance was recorded.
    rescore: z
      .object({
        sourceRescoreDigest: z.string().startsWith("sha256:"),
        scorerId: Id,
        scorerVersion: Version,
        scorerDigest: z.string().startsWith("sha256:"),
      })
      .strict()
      .optional(),
    source: ManifestSchema.shape.source,
    compatibility: ManifestSchema.shape.compatibility,
    datasetFingerprint: ManifestSchema.shape.datasetFingerprint,
    resolvedVariants: ManifestSchema.shape.resolvedVariants,
    effectiveConfig: EvalConfigSchema,
    plannedSlotKeys: ManifestSchema.shape.plannedSlotKeys,
    expectedSlotKeys: z.array(z.string()),
    files: z.record(z.string(), z.string().startsWith("sha256:")),
  })
  .strict();

export type EvalConfig = z.infer<typeof EvalConfigSchema>;
export type PricingCatalog = z.infer<typeof PricingCatalogSchema>;
export type EvalVariant = z.infer<typeof VariantSchema>;
export type EvalDataset = z.infer<typeof DatasetSchema>;
export type EvalCase = z.infer<typeof CaseSchema>;
export type EvalSuite = z.infer<typeof SuiteSchema>;
export type EvalThresholdPolicy = z.infer<typeof ThresholdPolicySchema>;
export type SemanticRegistry = z.infer<typeof SemanticRegistrySchema>;
export type EvalUnsafeEffect = z.infer<typeof UnsafeEffectSchema>;
export type EvalSecurityOutcome = z.infer<typeof SecurityOutcomeSchema>;
export type EvalAssertionTarget = z.infer<typeof AssertionTargetSchema>;
export type EvalScore = z.infer<typeof ScoreSchema>;
export type EvalAttempt = z.infer<typeof AttemptSchema>;
export type EvalSemanticEvidenceEvent = z.infer<
  typeof EvalSemanticEvidenceEventSchema
>;
export type EvalSemanticEvidence = z.infer<typeof EvalSemanticEvidenceSchema>;
export type EvalEpisode = z.infer<typeof EpisodeSchema>;
export type EvalManifest = z.infer<typeof ManifestSchema>;
export type EvalOracleJournal = z.infer<typeof OracleJournalSchema>;
export type EvalUsage = z.infer<typeof UsageSchema>;
export type EvalGenerator = z.infer<typeof GeneratorSchema>;
export type EvalSummaryDocument = z.infer<typeof SummarySchema>;
export type EvalResultLine = z.infer<typeof ResultLineSchema>;
export type EvalResultManifest = z.infer<typeof ResultManifestSchema>;
