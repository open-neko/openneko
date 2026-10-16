import {
  and,
  asc,
  db,
  eq,
  inArray,
  ne,
  run_channel_message,
  run_verification,
  work_message,
  work_run,
  work_run_event,
  work_thread,
} from "@neko/db";
import type { AgentEvent } from "../agent-backend";
import { GRAPHJIN_TOOL_PATTERN, LEARNING_THREAD_CHANNEL } from "./learn-target";
import { LEARNED_END, LEARNED_START } from "./skill-overlay";
import {
  isVerifiedWrong,
  runAnswerChecks,
  verificationLines,
  type RunVerification,
} from "./skill-learn-verify";

export type RunOutcome = "completed" | "failed" | "needs_input" | "cancelled" | "unknown";

export type RunEvidence = {
  runId: string;
  outcome: RunOutcome;
  error?: string;
  toolErrors: number;
  answer: string;
  followUp?: string;
  /** A deterministic check failed or an independent recomputation disagreed. */
  verifiedWrong?: boolean;
  /** People's channel messages the run consumed; they count as feedback. */
  feedback?: string[];
  /** Agent channel messages the run consumed; claims, never grounding. */
  claims?: number;
  digest: string;
  /** The text a rule quote must match; the digest without agent claims. */
  grounding?: string;
};

export type ChannelMessageEvidence = {
  senderKind: "human" | "agent";
  thread: string;
  content: string;
};

/** Only org-wide channel messages feed shared learned stores. */
export const EVIDENCE_CHANNEL_SCOPE = "org";

/** A run that failed, hit tool errors, drew an operator follow-up or failed verification. */
export function runWentWrong(run: RunEvidence): boolean {
  return run.outcome === "failed" || run.toolErrors > 0 || Boolean(run.followUp) || Boolean(run.verifiedWrong);
}

const MAX_STEPS = 40;
const MAX_INPUT = 240;
const MAX_RESULT = 320;
/** Data query steps hold the data lessons, so they keep more of the query and its result. */
const MAX_DATA_INPUT = 800;
const MAX_DATA_RESULT = 600;
const DATA_TOOL_RE = new RegExp(GRAPHJIN_TOOL_PATTERN, "i");
const MAX_REQUEST = 800;
const MAX_ANSWER = 1200;

export function buildRunEvidence(input: {
  runId: string;
  status: string;
  error?: string | null;
  request: string;
  answer: string;
  followUp?: string;
  events: readonly AgentEvent[];
  verifications?: readonly RunVerification[];
  channel?: readonly ChannelMessageEvidence[];
}): RunEvidence {
  const outcome = toOutcome(input.status);
  const starts = new Map<string, { name: string; input: string }>();
  const steps: string[] = [];
  let toolErrors = 0;
  for (const event of input.events) {
    if (steps.length >= MAX_STEPS) break;
    switch (event.type) {
      case "tool_start":
        starts.set(event.id, {
          name: event.name,
          input: clip(stringify(event.input), DATA_TOOL_RE.test(event.name) ? MAX_DATA_INPUT : MAX_INPUT),
        });
        break;
      case "tool_end": {
        const start = starts.get(event.id);
        const name = start?.name ?? "tool";
        const maxResult = DATA_TOOL_RE.test(name) ? MAX_DATA_RESULT : MAX_RESULT;
        if (event.error) {
          toolErrors += 1;
          steps.push(`${name}(${start?.input ?? ""}) -> error: ${clip(event.error, maxResult)}`);
        } else {
          steps.push(`${name}(${start?.input ?? ""}) -> ${clip(stringify(event.result), maxResult)}`);
        }
        break;
      }
      case "capability_denied":
        steps.push(`capability denied: ${clip(stringify(event), MAX_RESULT)}`);
        break;
      case "retry":
        steps.push(`retry: ${clip(event.reason, MAX_RESULT)}`);
        break;
      case "error":
        steps.push(`error: ${clip(event.message, MAX_RESULT)}`);
        break;
      case "needs_input":
        steps.push("asked the operator a clarification question");
        break;
      default:
        break;
    }
  }
  const answer = clip(input.answer, MAX_ANSWER);
  const verifications = input.verifications ?? [];
  const verifiedWrong = isVerifiedWrong(verifications);
  const channel = input.channel ?? [];
  const feedback = channel
    .filter((message) => message.senderKind === "human")
    .map((message) => clip(message.content, MAX_REQUEST));
  const feedbackLines = channel
    .filter((message) => message.senderKind === "human")
    .map((message) => `channel feedback from a person in ${message.thread}: ${clip(message.content, MAX_REQUEST)}`);
  const claimLines = channel
    .filter((message) => message.senderKind === "agent")
    .map((message) => `agent claim in ${message.thread}, not evidence: ${clip(message.content, MAX_RESULT)}`);
  const lines = [
    `run ${input.runId}: outcome ${outcome}${input.error ? ` (${clip(input.error, MAX_RESULT)})` : ""}, tool errors ${toolErrors}${verifiedWrong ? ", failed verification" : ""}`,
    `request: ${clip(input.request, MAX_REQUEST)}`,
    ...steps.map((step, index) => `step ${index + 1}: ${step}`),
    `answer: ${answer}`,
    ...verificationLines(verifications).map((line) => clip(line, MAX_RESULT)),
    ...(input.followUp ? [`operator follow-up: ${clip(input.followUp, MAX_REQUEST)}`] : []),
    ...feedbackLines,
  ];
  return {
    runId: input.runId,
    outcome,
    ...(input.error ? { error: input.error } : {}),
    toolErrors,
    answer,
    ...(input.followUp ? { followUp: clip(input.followUp, MAX_REQUEST) } : {}),
    ...(verifiedWrong ? { verifiedWrong } : {}),
    ...(feedback.length > 0 ? { feedback } : {}),
    ...(claimLines.length > 0
      ? { claims: claimLines.length, digest: [...lines, ...claimLines].join("\n"), grounding: lines.join("\n") }
      : { digest: lines.join("\n") }),
  };
}

export async function loadRunEvidence(
  orgId: string,
  runIds: readonly string[],
): Promise<RunEvidence[]> {
  if (runIds.length === 0) return [];
  const runs = await db()
    .select({
      id: work_run.id,
      threadId: work_run.thread_id,
      status: work_run.status,
      error: work_run.error,
      createdAt: work_run.created_at,
    })
    .from(work_run)
    .innerJoin(work_thread, eq(work_thread.id, work_run.thread_id))
    .where(
      and(
        eq(work_run.org_id, orgId),
        inArray(work_run.id, [...runIds]),
        ne(work_thread.channel, LEARNING_THREAD_CHANNEL),
      ),
    );
  if (runs.length === 0) return [];
  const messages = await db()
    .select({
      runId: work_message.run_id,
      threadId: work_message.thread_id,
      role: work_message.role,
      content: work_message.content,
      createdAt: work_message.created_at,
    })
    .from(work_message)
    .where(
      and(
        eq(work_message.org_id, orgId),
        inArray(work_message.thread_id, [...new Set(runs.map((run) => run.threadId))]),
      ),
    )
    .orderBy(asc(work_message.created_at));
  const events = await db()
    .select({ runId: work_run_event.run_id, payload: work_run_event.payload })
    .from(work_run_event)
    .where(
      and(
        eq(work_run_event.org_id, orgId),
        inArray(work_run_event.run_id, [...runIds]),
        inArray(work_run_event.kind, [
          "tool_start",
          "tool_end",
          "capability_denied",
          "retry",
          "error",
          "needs_input",
        ]),
      ),
    )
    .orderBy(asc(work_run_event.id));
  const stored = await db()
    .select({
      runId: run_verification.run_id,
      kind: run_verification.kind,
      checkId: run_verification.check_id,
      verdict: run_verification.verdict,
      detail: run_verification.detail,
    })
    .from(run_verification)
    .where(and(eq(run_verification.org_id, orgId), inArray(run_verification.run_id, [...runIds])))
    .orderBy(asc(run_verification.kind), asc(run_verification.check_id));
  const channelRows = await db()
    .select({
      runId: run_channel_message.run_id,
      senderKind: run_channel_message.sender_kind,
      thread: run_channel_message.channel_thread,
      content: run_channel_message.content,
    })
    .from(run_channel_message)
    .where(
      and(
        eq(run_channel_message.org_id, orgId),
        inArray(run_channel_message.run_id, [...runIds]),
        eq(run_channel_message.access_scope, EVIDENCE_CHANNEL_SCOPE),
      ),
    )
    .orderBy(asc(run_channel_message.created_at));

  const order = new Map(runIds.map((id, index) => [id, index]));
  runs.sort((a, b) => (order.get(a.id) ?? 0) - (order.get(b.id) ?? 0));
  return runs.map((run) => {
    const own = messages.filter((message) => message.runId === run.id);
    const last = own.at(-1)?.createdAt;
    const followUp = last
      ? messages.find(
          (message) =>
            message.threadId === run.threadId &&
            message.role === "user" &&
            message.runId !== run.id &&
            message.createdAt > last,
        )?.content
      : undefined;
    const request = own.filter((m) => m.role === "user").map((m) => m.content).join("\n");
    const answer = own.filter((m) => m.role === "assistant").map((m) => m.content).join("\n");
    const runEvents = events.filter((event) => event.runId === run.id).map((event) => event.payload as AgentEvent);
    const verifications: RunVerification[] = stored.filter((row) => row.runId === run.id);
    if (run.status === "completed" && !verifications.some((row) => row.kind === "check")) {
      for (const check of runAnswerChecks({
        request,
        answer: stripLearnerOutput(answer),
        events: runEvents,
        runAt: run.createdAt,
      })) {
        verifications.push({ kind: "check", checkId: check.id, verdict: check.verdict, detail: check.detail });
      }
    }
    return buildRunEvidence({
      runId: run.id,
      status: run.status,
      error: run.error,
      request,
      answer,
      followUp,
      events: runEvents,
      verifications,
      channel: channelRows.filter((row) => row.runId === run.id),
    });
  });
}

function toOutcome(status: string): RunOutcome {
  return status === "completed" ||
    status === "failed" ||
    status === "needs_input" ||
    status === "cancelled"
    ? status
    : "unknown";
}

function stringify(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

const LEARNED_SECTION_RE = new RegExp(`${escapeRe(LEARNED_START)}[\\s\\S]*?(?:${escapeRe(LEARNED_END)}|$)`, "g");
const CITATION_LINE_RE = /^.*applied learned rules?\b.*$/gim;
const RULE_KEY_RE = /\[?\b(?:skill|source|org):[A-Za-z0-9._-]+:L\d+\b\]?/g;
const LEARNED_BLOCK_RE = /<(learned_data_notes|org_playbook)>[\s\S]*?(?:<\/\1>|$)/g;

/** Learner output never counts as evidence: learned sections, rule ids and citations are cut. */
export function stripLearnerOutput(text: string): string {
  return text
    .replace(LEARNED_SECTION_RE, " ")
    .replace(LEARNED_BLOCK_RE, " ")
    .replace(CITATION_LINE_RE, " ")
    .replace(RULE_KEY_RE, " ");
}

function escapeRe(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function clip(text: string, max: number): string {
  const flat = stripLearnerOutput(text).replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}
