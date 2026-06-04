import { z } from "zod";

// A workflow trigger that fires when a row in the operator's data source
// matches a filter (the "do Y when data X changes" half). Lives inside
// `triggers.when`, alongside cron — so a single workflow save defines both
// the response and its data-change trigger. Stored as a subscription row.
export const SOURCE_CHANGE_TRIGGER_SCHEMA = z.object({
  table: z.string().trim().min(1).max(120),
  where: z.record(z.string(), z.unknown()).optional(),
  select: z.array(z.string().trim().min(1).max(120)).optional(),
  primary_key: z.array(z.string().trim().min(1).max(120)).min(1).max(8),
  version_column: z.string().trim().min(1).max(120).optional(),
  enabled: z.boolean().optional(),
  idempotency_key_template: z.string().max(200).optional(),
  acknowledge_mutation_loop: z.boolean().optional(),
});

export type SourceChangeTriggerPayload = z.infer<
  typeof SOURCE_CHANGE_TRIGGER_SCHEMA
>;

export const WORKFLOW_SAVE_SCHEMA = z.object({
  name: z.string().trim().min(1).max(120),
  description: z.string().max(2000).optional(),
  goal: z.string().max(2000).optional(),
  systemPromptOverlay: z.string().max(16000).optional(),
  steps: z
    .array(
      z.object({
        id: z.string().trim().min(1).max(60),
        description: z.string().trim().min(1).max(2000),
      }),
    )
    .min(1)
    .max(40),
  triggers: z
    .object({
      cron: z.string().trim().min(1).max(120).optional(),
      timezone: z.string().trim().min(1).max(80).optional(),
      enabled: z.boolean().optional(),
      when: SOURCE_CHANGE_TRIGGER_SCHEMA.optional(),
    })
    .optional(),
});

export type WorkflowSavePayload = z.infer<typeof WORKFLOW_SAVE_SCHEMA>;

export const OUTPUT_KINDS = [
  "report",
  "summary",
  "briefing_card_proposal",
  "chart",
  "table",
  "file",
  "message_draft",
  "finding",
  "observation",
  "recommendation",
] as const;

export const MOODS = ["good", "watch", "act"] as const;

export const WORKFLOW_OUTPUT_SCHEMA = z.object({
  kind: z.enum(OUTPUT_KINDS),
  title: z.string().max(240).optional(),
  body: z.string().max(64_000).optional(),
  payload: z.record(z.string(), z.unknown()).optional(),
  artifactPath: z.string().max(1024).optional(),
  scope: z.string().max(120).optional(),
  topic: z.string().max(120).optional(),
  mood: z.enum(MOODS).optional(),
  timeWindowStart: z.string().datetime().optional(),
  timeWindowEnd: z.string().datetime().optional(),
  freshnessTtlSeconds: z.number().int().positive().max(31_536_000).optional(),
});

export type WorkflowOutputPayload = z.infer<typeof WORKFLOW_OUTPUT_SCHEMA>;

export const RISK_LEVELS = ["low", "medium", "high", "critical"] as const;
export const ACTION_SCOPES = ["internal", "external"] as const;

// Policy CRUD via Ask. Mirrors the workflow-save fence pattern. The agent
// describes a policy in plain English with the user, then commits via this
// fence. createActionPolicy seeds the row; if a policy with the same name
// exists, the upsert path updates it in place.
export const POLICY_MODES = [
  "observe_only",
  "draft_only",
  "auto_approve",
  "approval_required",
  "never",
] as const;

export const POLICY_SAVE_SCHEMA = z.object({
  name: z.string().trim().min(1).max(120),
  description: z.string().max(2000).optional(),
  applies_to_kinds: z.array(z.string().trim().min(1).max(120)).min(0).max(40),
  applies_to_scopes: z
    .array(z.enum(ACTION_SCOPES))
    .min(0)
    .max(8)
    .default(["external"] as ("internal" | "external")[]),
  mode: z.enum(POLICY_MODES),
  risk_threshold_auto_approve: z.enum(RISK_LEVELS).optional(),
  allowed_targets: z.record(z.string(), z.unknown()).optional(),
  denied_targets: z.record(z.string(), z.unknown()).optional(),
  limits: z.record(z.string(), z.unknown()).default({}),
  approver_role: z.string().trim().max(120).optional(),
  priority: z.number().int().min(0).max(10_000).default(100),
  enabled: z.boolean().default(true),
});

export type PolicySavePayload = z.infer<typeof POLICY_SAVE_SCHEMA>;

export const ACTION_REQUEST_SCHEMA = z.object({
  scope: z.enum(ACTION_SCOPES),
  kind: z.string().trim().min(1).max(120),
  target: z.string().trim().min(1).max(1024).optional(),
  payload: z.record(z.string(), z.unknown()).optional(),
  risk_level: z.enum(RISK_LEVELS).optional(),
  summary: z.string().trim().min(1).max(2000),
  minutes_saved: z
    .number()
    .int()
    .min(0)
    .max(600)
    .optional()
    .describe(
      "Conservative estimate of the minutes a competent human would spend doing this one action by hand (e.g. send email ~8, file refund ~15). Omit if unsure.",
    ),
  basis: z
    .string()
    .trim()
    .max(160)
    .optional()
    .describe("One short line naming why minutes_saved is what it is."),
});

export type ActionRequestPayload = z.infer<typeof ACTION_REQUEST_SCHEMA>;

// Per-run analysis value estimate. Emitted once at the end of every run via
// a `neko_value` fence (excludes per-action work, which carries its own
// estimate). Server-clamped before persisting. See docs/HOURS_SAVED_PLAN.md.
export const VALUE_ESTIMATE_SCHEMA = z.object({
  minutes_saved: z.number().int().min(0).max(600),
  basis: z.string().trim().max(160).optional(),
});

export type ValueEstimatePayload = z.infer<typeof VALUE_ESTIMATE_SCHEMA>;

// Structured context for the Ask page's right rail. Emitted once at the end of
// a /work run via a `neko_ask_context` fence. Everything optional — the rail
// renders whichever sections are present.
export const ASK_CONTEXT_SCHEMA = z.object({
  vitals: z
    .array(
      z.object({
        label: z.string().trim().min(1).max(40),
        value: z.string().trim().min(1).max(40),
        sub: z.string().trim().max(40).optional(),
      }),
    )
    .max(4)
    .optional(),
  sources: z
    .array(
      z.object({
        name: z.string().trim().min(1).max(80),
        detail: z.string().trim().max(40).optional(),
      }),
    )
    .max(8)
    .optional(),
  followups: z.array(z.string().trim().min(1).max(120)).max(4).optional(),
});

export type AskContextPayload = z.infer<typeof ASK_CONTEXT_SCHEMA>;
