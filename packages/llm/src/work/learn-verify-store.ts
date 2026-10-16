import {
  and,
  asc,
  db,
  eq,
  gte,
  inArray,
  run_verification,
  sql,
  work_message,
  work_run,
  work_run_event,
  work_thread,
} from "@neko/db";
import type { AgentEvent } from "../agent-backend";
import { LEARNING_THREAD_CHANNEL } from "./learn-target";
import { stripLearnerOutput } from "./skill-learn-evidence";
import {
  compareRecompute,
  numberAtPath,
  runAnswerChecks,
  runDataQueries,
  sampledForVerify,
  significantNumbers,
  verifyRate,
  type AnswerCheck,
  type RecomputeVerdict,
} from "./skill-learn-verify";

export const RECOMPUTE_CHECK_ID = "headline";
export const DEFAULT_DAILY_VERIFICATIONS = 48;
const DAY_MS = 24 * 60 * 60 * 1000;
const MAX_CATALOG_CHARS = 12000;
const MAX_REQUEST_CHARS = 2000;
const MAX_ANSWER_CHARS = 4000;

export type VerifyPromptInput = {
  request: string;
  answer: string;
  answerFigures: string[];
  catalog: string;
  runQueries: string[];
};

export type VerifyProposal = {
  quantity: string;
  answerFigure: string;
  query: string;
  resultPath: string;
};

export type VerifyLlm = (input: VerifyPromptInput) => Promise<VerifyProposal | null>;
export type VerifyExecute = (input: {
  orgId: string;
  runId: string;
  query: string;
}) => Promise<{ data: unknown; errors?: Array<{ message: string }> }>;

type RunRecord = {
  status: string;
  createdAt: Date;
  request: string;
  answer: string;
  events: AgentEvent[];
};

async function loadRun(orgId: string, runId: string): Promise<RunRecord | null> {
  const [run] = await db()
    .select({ status: work_run.status, createdAt: work_run.created_at, channel: work_thread.channel })
    .from(work_run)
    .innerJoin(work_thread, eq(work_thread.id, work_run.thread_id))
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (!run || run.channel === LEARNING_THREAD_CHANNEL) return null;
  const [messages, events] = await Promise.all([
    db()
      .select({ role: work_message.role, content: work_message.content })
      .from(work_message)
      .where(and(eq(work_message.org_id, orgId), eq(work_message.run_id, runId)))
      .orderBy(asc(work_message.created_at)),
    db()
      .select({ payload: work_run_event.payload })
      .from(work_run_event)
      .where(
        and(
          eq(work_run_event.org_id, orgId),
          eq(work_run_event.run_id, runId),
          inArray(work_run_event.kind, ["tool_start", "tool_end"]),
        ),
      )
      .orderBy(asc(work_run_event.id)),
  ]);
  return {
    status: run.status,
    createdAt: run.createdAt,
    request: messages.filter((m) => m.role === "user").map((m) => m.content).join("\n"),
    answer: stripLearnerOutput(messages.filter((m) => m.role === "assistant").map((m) => m.content).join("\n")),
    events: events.map((event) => event.payload as AgentEvent),
  };
}

async function upsertVerification(input: {
  orgId: string;
  runId: string;
  kind: "check" | "recompute";
  checkId: string;
  verdict: string;
  detail: string;
  data?: Record<string, unknown>;
}): Promise<void> {
  const now = new Date();
  await db()
    .insert(run_verification)
    .values({
      org_id: input.orgId,
      run_id: input.runId,
      kind: input.kind,
      check_id: input.checkId,
      verdict: input.verdict as typeof run_verification.$inferInsert.verdict,
      detail: input.detail,
      data: input.data ?? {},
      updated_at: now,
    })
    .onConflictDoUpdate({
      target: [run_verification.run_id, run_verification.kind, run_verification.check_id],
      set: {
        verdict: input.verdict as typeof run_verification.$inferInsert.verdict,
        detail: input.detail,
        data: input.data ?? {},
        updated_at: now,
      },
    });
}

/** Runs the deterministic answer checks on a completed run and stores the verdicts. */
export async function recordAnswerChecks(orgId: string, runId: string): Promise<AnswerCheck[]> {
  const run = await loadRun(orgId, runId);
  if (!run || run.status !== "completed") return [];
  const checks = runAnswerChecks({ request: run.request, answer: run.answer, events: run.events, runAt: run.createdAt });
  for (const check of checks) {
    await upsertVerification({ orgId, runId, kind: "check", checkId: check.id, verdict: check.verdict, detail: check.detail });
  }
  return checks;
}

function dailyVerificationLimit(): number {
  const value = Number(process.env.OPENNEKO_LEARN_DAILY_VERIFICATIONS);
  return Number.isFinite(value) && value >= 0 ? Math.floor(value) : DEFAULT_DAILY_VERIFICATIONS;
}

/**
 * Marks a sampled, completed run with a numeric answer as waiting for an
 * independent recomputation. The pending row holds the run's settlement
 * until the job finishes or times out.
 */
export async function scheduleRecompute(
  orgId: string,
  runId: string,
  options: { rate?: number; now?: Date } = {},
): Promise<boolean> {
  if (!sampledForVerify(runId, options.rate ?? verifyRate())) return false;
  const run = await loadRun(orgId, runId);
  if (!run || run.status !== "completed" || significantNumbers(run.answer).length === 0) return false;
  const since = new Date((options.now ?? new Date()).getTime() - DAY_MS);
  const [count] = await db()
    .select({ n: sql<number>`count(*)::int` })
    .from(run_verification)
    .where(
      and(
        eq(run_verification.org_id, orgId),
        eq(run_verification.kind, "recompute"),
        gte(run_verification.created_at, since),
      ),
    );
  if ((count?.n ?? 0) >= dailyVerificationLimit()) return false;
  const inserted = await db()
    .insert(run_verification)
    .values({ org_id: orgId, run_id: runId, kind: "recompute", check_id: RECOMPUTE_CHECK_ID, verdict: "pending" })
    .onConflictDoNothing()
    .returning({ id: run_verification.id });
  return inserted.length > 0;
}

async function catalogFor(orgId: string): Promise<string> {
  try {
    const { ensureOrgWorkspace } = await import("./workspace");
    const { knowledgePackPaths, readKnowledgePack } = await import("../knowledge-pack");
    const pack = await readKnowledgePack(knowledgePackPaths((await ensureOrgWorkspace(orgId)).knowledgeRoot));
    const parts =
      pack.mode === "agentic"
        ? [`GraphJin syntax:\n${pack.syntax}`, `Tables and join paths:\n${pack.insights}`]
        : [`Tables:\n${pack.tables}`, `Insights:\n${pack.insights}`, `GraphJin syntax:\n${pack.syntax}`];
    return parts.join("\n\n").slice(0, MAX_CATALOG_CHARS);
  } catch {
    return "";
  }
}

async function defaultExecute(input: { orgId: string; runId: string; query: string }) {
  const { inProcessControlPlane } = await import("./control-plane");
  return inProcessControlPlane.queryGraphjinRead({ orgId: input.orgId, runId: input.runId, query: input.query });
}

async function defaultVerifyLlm(orgId: string): Promise<VerifyLlm> {
  const [{ ax }, { buildLlm }, { normalizeAxProgramUsage }, { estimateTokenCostUsd, MODEL_PRICES_VERSION }, { recordUsageSpend }] =
    await Promise.all([
      import("@ax-llm/ax"),
      import("../llm"),
      import("../usage-normalization"),
      import("../spend/model-prices"),
      import("../spend/ledger"),
    ]);
  const llm = await buildLlm(orgId);
  const generator = ax(
    `verifyRequest:string "the question the analyst answered", answerText:string "the analyst's answer", answerFigures:string[] "figures stated in the answer", sourceCatalog:string "tables, join paths and GraphJin syntax of the data source", runQueries:string[] "the analyst's own queries, for reference" -> quantityName:string "a short name for the headline quantity, such as total revenue", answerFigure:string "the figure from answerFigures that states the headline quantity, copied exactly", graphqlQuery:string "one read-only GraphQL query that computes that quantity independently", resultPath:string "dot path to the number in the query result, such as orders.0.sum_grand_total"`,
    {
      description:
        "You check an analyst's answer by computing its headline figure a second, independent way. Pick the figure that answers the question. Write one read-only GraphQL query that computes the same quantity through a different table, join path or aggregation grain than the analyst's queries. Use aggregate fields so the query returns the figure itself, and keep the same filters and time window that the question asks for.",
    },
  );
  return async (input) => {
    const result = await generator.forward(llm, {
      verifyRequest: input.request,
      answerText: input.answer,
      answerFigures: input.answerFigures,
      sourceCatalog: input.catalog || "(no catalog)",
      runQueries: input.runQueries.length > 0 ? input.runQueries : ["(none)"],
    });
    const calls = generator.getUsage();
    const usage = normalizeAxProgramUsage(calls);
    const last = calls.at(-1);
    const cost =
      last?.ai && last.model
        ? estimateTokenCostUsd(last.ai, last.model, usage)
        : undefined;
    await recordUsageSpend({
      orgId,
      source: "system",
      usage: cost === undefined
        ? { ...usage, costStatus: "unknown" }
        : { ...usage, costStatus: "estimated", estimatedCostUsd: cost, currency: "USD", costSource: "official_docs_snapshot", pricingCatalogVersion: MODEL_PRICES_VERSION },
      ...(last?.ai ? { provider: last.ai } : {}),
      ...(last?.model ? { model: last.model } : {}),
    }).catch((error) =>
      console.warn(`[learn-verify] spend record failed: ${error instanceof Error ? error.message : error}`),
    );
    const query = String(result.graphqlQuery ?? "").trim();
    if (!query) return null;
    return {
      quantity: String(result.quantityName ?? "").trim(),
      answerFigure: String(result.answerFigure ?? "").trim(),
      query,
      resultPath: String(result.resultPath ?? "").trim(),
    };
  };
}

function clip(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

/** One independent recomputation of a sampled run's headline figure; never posts anywhere. */
export async function runRecompute(input: {
  orgId: string;
  runId: string;
  llm?: VerifyLlm;
  execute?: VerifyExecute;
  tolerance?: number;
  /** Recompute even when the run holds no pending row. */
  force?: boolean;
}): Promise<{ verdict: RecomputeVerdict; detail: string } | null> {
  const [row] = await db()
    .select({ verdict: run_verification.verdict })
    .from(run_verification)
    .where(
      and(
        eq(run_verification.org_id, input.orgId),
        eq(run_verification.run_id, input.runId),
        eq(run_verification.kind, "recompute"),
      ),
    )
    .limit(1);
  if (!input.force && row?.verdict !== "pending") return null;
  const run = await loadRun(input.orgId, input.runId);
  if (!run) return null;
  const figures = significantNumbers(run.answer);
  const finish = async (verdict: RecomputeVerdict, detail: string, data: Record<string, unknown> = {}) => {
    await upsertVerification({
      orgId: input.orgId,
      runId: input.runId,
      kind: "recompute",
      checkId: RECOMPUTE_CHECK_ID,
      verdict,
      detail,
      data,
    });
    return { verdict, detail };
  };
  if (run.status !== "completed" || figures.length === 0) return finish("unknown", "answer states no figure");

  let proposal: VerifyProposal | null;
  try {
    const llm = input.llm ?? (await defaultVerifyLlm(input.orgId));
    proposal = await llm({
      request: clip(run.request, MAX_REQUEST_CHARS),
      answer: clip(run.answer, MAX_ANSWER_CHARS),
      answerFigures: figures.map((figure) => figure.text),
      catalog: await catalogFor(input.orgId),
      runQueries: runDataQueries(run.events),
    });
  } catch (error) {
    return finish("unknown", `verifier model failed: ${clip(error instanceof Error ? error.message : String(error), 200)}`);
  }
  if (!proposal) return finish("unknown", "verifier wrote no query");
  const target = figures.find((figure) => figure.text === proposal!.answerFigure) ??
    significantNumbers(proposal.answerFigure).find((picked) =>
      figures.some((figure) => Math.abs(figure.value - picked.value) <= figure.tolerance),
    );
  if (!target) {
    return finish("unknown", `verifier picked ${clip(proposal.answerFigure, 40)}, which the answer does not state`, {
      query: proposal.query,
    });
  }
  let value: number | null;
  try {
    const { assertReadOnlyGraphql } = await import("./control-plane");
    assertReadOnlyGraphql(proposal.query);
    const response = await (input.execute ?? defaultExecute)({
      orgId: input.orgId,
      runId: input.runId,
      query: proposal.query,
    });
    if (response.errors?.length) {
      return finish("unknown", `independent query failed: ${clip(response.errors.map((e) => e.message).join("; "), 200)}`, {
        query: proposal.query,
      });
    }
    value = numberAtPath(response.data, proposal.resultPath);
  } catch (error) {
    return finish("unknown", `independent query failed: ${clip(error instanceof Error ? error.message : String(error), 200)}`, {
      query: proposal.query,
    });
  }
  const verdict = compareRecompute({
    answerValue: target.text,
    recomputed: value,
    tolerance: input.tolerance,
    quantity: proposal.quantity,
  });
  return finish(verdict.verdict, verdict.detail, {
    query: proposal.query,
    resultPath: proposal.resultPath,
    quantity: proposal.quantity,
    answerFigure: target.text,
    value,
  });
}

/** Both signals for one run, inline: the deterministic checks and a forced recomputation. */
export async function verifyRunForLearning(input: {
  orgId: string;
  runId: string;
  llm?: VerifyLlm;
  execute?: VerifyExecute;
}): Promise<{ checks: AnswerCheck[]; recompute: { verdict: RecomputeVerdict; detail: string } | null }> {
  const checks = await recordAnswerChecks(input.orgId, input.runId);
  const recompute = await runRecompute({ ...input, force: true });
  return { checks, recompute };
}
