import { and, db, eq, llm_provider_config } from "@neko/db";
import { maybeDecryptSecret } from "../secrets";
import {
  WORK_MEMORY_KINDS,
  createPendingWorkMemory,
  findConflictingWorkMemories,
  normalizeNewWorkMemoryScope,
  rememberWorkMemory,
  type WorkMemory,
  type WorkMemoryContext,
  type WorkMemoryKind,
  type WorkMemoryScope,
  type WorkPendingMemory,
  type WorkPendingMemoryConflict,
} from "./memory";

export type WorkAutoMemoryMode = "off" | "propose" | "on";

export type WorkMemoryDraft = {
  text: string;
  kind: WorkMemoryKind;
  scope: WorkMemoryScope;
  scopeId?: string | null;
  confidence: number;
  reasoning?: string;
};

export type WorkMemoryClassifierInput = WorkMemoryContext & {
  userMessage: string;
  agentAnswer: string;
};

export type WorkMemoryDispatchResult = {
  saved: WorkMemory[];
  pending: WorkPendingMemory[];
  skipped: Array<{ draft: WorkMemoryDraft; reason: string }>;
};

export type WorkMemoryDraftDisposition = "save" | "pending" | "skip";

const HIGH_CONFIDENCE_THRESHOLD = 0.92;
const REVIEW_CONFIDENCE_THRESHOLD = 0.86;
const MIN_DRAFT_CONFIDENCE = 0.65;
const FALLBACK_CLASSIFIER_MODEL = "claude-haiku-4-5-20251001";

const CLASSIFIER_SYSTEM_PROMPT = `You are an auto-memory classifier for Neko Work.

Given the user's latest message and the agent's reply, decide whether the turn
contains a durable fact that should affect future business-analysis sessions.

Be conservative. Only propose memories when the USER explicitly signals durable
intent, using language like "remember", "always", "never", "from now on",
"going forward", "default to", "save this", "note that", or a clear correction
that changes a standing business rule.

Good memories:
- Stable preferences: "always show revenue in INR lakhs"
- Business rules: "from now on, exclude acme.test customers"
- Metric definitions: "active customer means ordered in the last 90 days"
- Permanent corrections: "actually, net revenue excludes returns"
- Company context: "our fiscal year starts in April"

Do NOT propose memories for:
- Ordinary one-off filters or report requests
- Facts inferred only from the agent's answer
- Findings, numbers, or analysis output
- Vague hints that might be useful later
- Anything the user explicitly asked you to forget

When you do emit a draft, only use these scopes:
- "global" for durable rules that should apply everywhere
- "thread" for context that should only apply to this Work thread

Do NOT use "database" scope for auto-memory drafts.

Output via the propose_memories tool. Return at most 2 drafts. Empty drafts is
the right answer for most turns.`;

type ClassifierOptions = {
  apiKey?: string;
  model?: string;
  fetchImpl?: typeof fetch;
};

export function getWorkAutoMemoryMode(): WorkAutoMemoryMode {
  const raw = process.env.AGENT_AUTO_MEMORY?.trim().toLowerCase();
  if (raw === "off" || raw === "propose" || raw === "on") return raw;
  return "on";
}

export async function classifyTurnForWorkMemory(
  input: WorkMemoryClassifierInput,
  options: ClassifierOptions = {},
): Promise<WorkMemoryDraft[]> {
  const config = await resolveClassifierConfig(input.orgId, options);
  if (!config.apiKey) return [];
  const fetchImpl = options.fetchImpl ?? fetch;
  const userBlock = [
    `USER MESSAGE:\n${truncate(input.userMessage, 4000)}`,
    `\nAGENT ANSWER:\n${truncate(input.agentAnswer, 4000)}`,
    `\nTHREAD ID: ${input.threadId ?? "(none)"}`,
  ].join("\n");

  const body = {
    model: config.model,
    max_tokens: 900,
    system: CLASSIFIER_SYSTEM_PROMPT,
    tools: [
      {
        name: "propose_memories",
        description: "Emit zero or more durable memory drafts derived from this turn.",
        input_schema: {
          type: "object",
          properties: {
            drafts: {
              type: "array",
              maxItems: 2,
              items: {
                type: "object",
                properties: {
                  text: { type: "string", maxLength: 600 },
                  kind: { type: "string", enum: [...WORK_MEMORY_KINDS] },
                  scope: { type: "string", enum: ["global", "thread"] },
                  scopeId: { type: "string", maxLength: 200 },
                  confidence: { type: "number", minimum: 0, maximum: 1 },
                  reasoning: { type: "string", maxLength: 400 },
                },
                required: ["text", "kind", "scope", "confidence"],
              },
            },
          },
          required: ["drafts"],
        },
      },
    ],
    tool_choice: { type: "tool", name: "propose_memories" },
    messages: [{ role: "user", content: userBlock }],
  };

  const res = await fetchImpl("https://api.anthropic.com/v1/messages", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-api-key": config.apiKey,
      "anthropic-version": "2023-06-01",
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) return [];
  const json = (await res.json()) as { content?: Array<Record<string, unknown>> };
  const block = (json.content ?? []).find(
    (entry) => entry.type === "tool_use" && entry.name === "propose_memories",
  );
  if (!block) return [];
  const inputObj = (block.input ?? {}) as { drafts?: unknown[] };
  const drafts = Array.isArray(inputObj.drafts) ? inputObj.drafts : [];
  return drafts
    .filter((draft): draft is WorkMemoryDraft => isValidDraft(draft))
    .filter((draft) => draft.confidence >= MIN_DRAFT_CONFIDENCE)
    .slice(0, 2);
}

export async function dispatchWorkMemoryDrafts(
  drafts: WorkMemoryDraft[],
  ctx: WorkMemoryContext & { userMessage?: string },
  modeOverride?: WorkAutoMemoryMode,
): Promise<WorkMemoryDispatchResult> {
  const mode = modeOverride ?? getWorkAutoMemoryMode();
  const result: WorkMemoryDispatchResult = { saved: [], pending: [], skipped: [] };
  if (mode === "off" || drafts.length === 0) return result;

  for (const draft of drafts) {
    if (!isValidDraft(draft)) {
      result.skipped.push({ draft, reason: "invalid draft" });
      continue;
    }
    const normalizedScope = normalizeNewWorkMemoryScope(draft.scope, {
      threadId: ctx.threadId ?? null,
    });
    const scopeId = resolveScopeId(normalizedScope, draft, ctx);
    const conflicts = await findConflictingWorkMemories({
      orgId: ctx.orgId,
      text: draft.text,
      kind: draft.kind,
      scope: normalizedScope,
      scopeId,
    });
    const conflictSummaries: WorkPendingMemoryConflict[] = conflicts.map((conflict) => ({
      memoryId: conflict.memory.id,
      text: conflict.memory.text,
      similarity: round2(conflict.similarity),
    }));

    const disposition = decideWorkMemoryDraftDisposition({
      draft,
      mode,
      conflictCount: conflictSummaries.length,
      userMessage: ctx.userMessage,
    });

    if (disposition === "save") {
      const memory = await rememberWorkMemory({
        orgId: ctx.orgId,
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        text: draft.text,
        kind: draft.kind,
        scope: normalizedScope,
        scopeId,
        confidence: draft.confidence,
        metadata: { origin: "auto_memory_high_confidence" },
      });
      result.saved.push(memory);
      continue;
    }

    if (disposition === "pending") {
      const pending = await createPendingWorkMemory({
        orgId: ctx.orgId,
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        draftText: draft.text,
        draftKind: draft.kind,
        draftScope: normalizedScope,
        draftScopeId: scopeId,
        confidence: draft.confidence,
        reasoning: draft.reasoning,
        conflicts: conflictSummaries,
      });
      result.pending.push(pending);
      continue;
    }

    result.skipped.push({ draft, reason: "not explicit or confident enough" });
  }

  return result;
}

export function decideWorkMemoryDraftDisposition(input: {
  draft: WorkMemoryDraft;
  mode: WorkAutoMemoryMode;
  conflictCount: number;
  userMessage?: string;
}): WorkMemoryDraftDisposition {
  const { draft, mode, conflictCount, userMessage } = input;
  if (mode === "off" || draft.confidence < MIN_DRAFT_CONFIDENCE) return "skip";
  const explicit = hasExplicitMemorySignal(
    [userMessage, draft.text, draft.reasoning].filter(Boolean).join("\n"),
  );

  if (mode === "propose") {
    return explicit && draft.confidence >= REVIEW_CONFIDENCE_THRESHOLD ? "pending" : "skip";
  }

  if (
    conflictCount === 0 &&
    draft.confidence >= HIGH_CONFIDENCE_THRESHOLD &&
    (explicit || draft.confidence >= 0.97)
  ) {
    return "save";
  }

  if (explicit && draft.confidence >= REVIEW_CONFIDENCE_THRESHOLD) {
    return "pending";
  }

  return "skip";
}

export async function runWorkAutoMemoryPipeline(
  input: WorkMemoryClassifierInput,
): Promise<WorkMemoryDispatchResult | null> {
  const mode = getWorkAutoMemoryMode();
  if (mode === "off") return null;
  if (!input.userMessage.trim() || !input.agentAnswer.trim()) return null;

  try {
    const drafts = await classifyTurnForWorkMemory(input);
    return dispatchWorkMemoryDrafts(
      drafts,
      {
        orgId: input.orgId,
        threadId: input.threadId ?? null,
        runId: input.runId ?? null,
        userMessage: input.userMessage,
      },
      mode,
    );
  } catch (error) {
    console.error("[work-auto-memory] pipeline failed:", error);
    return null;
  }
}

export function hasExplicitMemorySignal(text: string): boolean {
  return /\b(remember|always|never|from now on|going forward|in future|for future|default to|save this|note that|keep in mind|correction|actually|we define|we call|treat .* as|should mean|means)\b/i.test(
    text,
  );
}

async function resolveClassifierConfig(
  orgId: string,
  options: ClassifierOptions,
): Promise<{ apiKey: string | null; model: string }> {
  if (options.apiKey) {
    return {
      apiKey: options.apiKey,
      model: options.model ?? process.env.AGENT_AUTO_MEMORY_MODEL ?? FALLBACK_CLASSIFIER_MODEL,
    };
  }
  if (process.env.ANTHROPIC_API_KEY?.trim()) {
    return {
      apiKey: process.env.ANTHROPIC_API_KEY.trim(),
      model: options.model ?? process.env.AGENT_AUTO_MEMORY_MODEL ?? FALLBACK_CLASSIFIER_MODEL,
    };
  }

  const rows = await db()
    .select({
      provider: llm_provider_config.provider,
      enabled: llm_provider_config.enabled,
      secrets: llm_provider_config.secrets,
    })
    .from(llm_provider_config)
    .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, "primary")))
    .limit(1);
  const row = rows[0];
  if (!row || row.provider !== "anthropic" || !row.enabled) {
    return {
      apiKey: null,
      model: options.model ?? process.env.AGENT_AUTO_MEMORY_MODEL ?? FALLBACK_CLASSIFIER_MODEL,
    };
  }
  const secrets = (row.secrets ?? {}) as Record<string, unknown>;
  const apiKey = maybeDecryptSecret(secrets.apiKey) ?? null;
  return {
    apiKey,
    model: options.model ?? process.env.AGENT_AUTO_MEMORY_MODEL ?? FALLBACK_CLASSIFIER_MODEL,
  };
}

function isValidDraft(draft: unknown): draft is WorkMemoryDraft {
  if (!draft || typeof draft !== "object") return false;
  const candidate = draft as Record<string, unknown>;
  if (typeof candidate.text !== "string" || candidate.text.trim().length < 4) return false;
  if (
    typeof candidate.kind !== "string" ||
    !WORK_MEMORY_KINDS.includes(candidate.kind as WorkMemoryKind)
  ) {
    return false;
  }
  if (
    typeof candidate.scope !== "string" ||
    (candidate.scope !== "global" && candidate.scope !== "thread")
  ) {
    return false;
  }
  if (
    typeof candidate.confidence !== "number" ||
    candidate.confidence < 0 ||
    candidate.confidence > 1
  ) {
    return false;
  }
  return true;
}

function resolveScopeId(
  scope: "global" | "thread",
  draft: WorkMemoryDraft,
  ctx: WorkMemoryContext,
): string | null {
  if (draft.scopeId) return draft.scopeId;
  if (scope === "thread") return ctx.threadId ?? null;
  return null;
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 3)}...`;
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
