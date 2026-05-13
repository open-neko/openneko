import { z } from "zod";

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

export const ACTION_REQUEST_SCHEMA = z.object({
  scope: z.enum(ACTION_SCOPES),
  kind: z.string().trim().min(1).max(120),
  target: z.string().trim().min(1).max(1024).optional(),
  payload: z.record(z.string(), z.unknown()).optional(),
  risk_level: z.enum(RISK_LEVELS).optional(),
  summary: z.string().trim().min(1).max(2000),
});

export type ActionRequestPayload = z.infer<typeof ACTION_REQUEST_SCHEMA>;
