import type { EvalModelRequest, EvalReplyWritten } from "./eval-install";

export type ResponderGrade = { due: boolean; passed: boolean; findings: string[] };

export type ResponderGrades = {
  closingBlocks: ResponderGrade;
  cards: ResponderGrade;
  noLeak: ResponderGrade;
};

export type ResponderGradeInput = {
  events: readonly Record<string, unknown>[];
  finalText: string;
  question: string;
  workflowRun: boolean;
  cardsEnabled: boolean;
  analysisMinutesSaved: number | null | undefined;
};

const CHART_OR_TABLE = /\b(?:chart|graph|plot|table)s?\b/iu;
const MACHINE_FENCE_NAME =
  /\bneko_(?:value|vitals|followups|cards|a2ui|action_request|workflow_save|rule_save|memory)\b/u;
const EXECUTOR_CODE = [
  /\b(?:final|askClarification|replyAsWritten|harnessSavedOperation)\s*\(/u,
  /"javascriptCode"\s*:/u,
  /\bglobalThis\.harnessEvidence\b/u,
  /\bconsole\.log\s*\(/u,
  /\bawait\s+[A-Za-z_$][\w$.]*\s*\(/u,
];
const CODE_FENCE = /```[ \t]*(?:javascript|js|typescript|ts)\b/iu;
export const CLOSED_MACHINE_FENCE = /```neko_[a-z_]+[\s\S]*?```/gu;

function ofType(events: readonly Record<string, unknown>[], type: string) {
  return events.filter((event) => event.type === type);
}

function nonEmptyList(value: unknown): boolean {
  return Array.isArray(value) && value.length > 0;
}

// The host emits a followups or vitals event only for a block that parsed,
// and stores the time-saved value only when its block parsed.
export function gradeClosingBlocks(input: ResponderGradeInput): ResponderGrade {
  const asked = ofType(input.events, "needs_input").length > 0;
  if (asked || !input.finalText.trim()) return { due: false, passed: true, findings: [] };
  const findings: string[] = [];
  if (typeof input.analysisMinutesSaved !== "number") findings.push("neko_value");
  if (!input.workflowRun) {
    const vitals = ofType(input.events, "vitals").at(-1);
    if (!vitals || !Array.isArray(vitals.items)) findings.push("neko_vitals");
    const followups = ofType(input.events, "followups").at(-1);
    if (!followups || !Array.isArray(followups.items)) findings.push("neko_followups");
  }
  return { due: true, passed: findings.length === 0, findings };
}

export function gradeCards(input: ResponderGradeInput): ResponderGrade {
  if (!input.cardsEnabled || !CHART_OR_TABLE.test(input.question)) {
    return { due: false, passed: true, findings: [] };
  }
  const surfaces = ofType(input.events, "surface").filter((event) => nonEmptyList(event.messages));
  return surfaces.length > 0
    ? { due: true, passed: true, findings: [] }
    : { due: true, passed: false, findings: ["no card surface"] };
}

// The stream carries the closing blocks; the web client hides each closed
// neko_ fence, so the user sees the rest.
export function gradeNoLeak(input: ResponderGradeInput): ResponderGrade {
  const text = input.finalText.replace(CLOSED_MACHINE_FENCE, "");
  if (!text.trim()) return { due: false, passed: true, findings: [] };
  const findings: string[] = [];
  if ((text.match(/```/gu)?.length ?? 0) % 2 !== 0) findings.push("unclosed fence");
  if (MACHINE_FENCE_NAME.test(text)) findings.push("machine block name");
  if (CODE_FENCE.test(text)) findings.push("code fence");
  if (EXECUTOR_CODE.some((pattern) => pattern.test(text))) findings.push("executor code");
  return { due: true, passed: findings.length === 0, findings };
}

export function gradeResponder(input: ResponderGradeInput): ResponderGrades {
  return {
    closingBlocks: gradeClosingBlocks(input),
    cards: gradeCards(input),
    noLeak: gradeNoLeak(input),
  };
}

// The executor also streams on thought-summary providers, so the stage names
// the responder; a trace written before stages has only the streamed mark.
export function responderMeasurements(
  requests: readonly EvalModelRequest[],
  replies: readonly EvalReplyWritten[] = [],
) {
  const staged = requests.some((request) => request.stage !== undefined);
  const responder = requests.filter((request) =>
    staged ? request.stage === "root/responder" : request.streamed,
  );
  const sum = (pick: (request: EvalModelRequest) => number | undefined) =>
    responder.reduce((total, request) => total + (pick(request) ?? 0), 0);
  const reported = responder.every(
    (request) => request.usage !== undefined && request.usage.coverage !== "partial",
  );
  const efforts = [
    ...new Set(requests.map((request) => request.responderReasoningEffort).filter(Boolean)),
  ];
  return {
    modelRequests: requests.length,
    responderCalls: responder.length,
    ...(responder.length
      ? {
          responderLatencyMs: sum((request) => request.durationMs),
          responderUsageReported: reported,
          responderModels: [...new Set(responder.map((request) => request.observedModel ?? request.model))],
        }
      : {}),
    ...(responder.length && reported
      ? {
          responderInputTokens: sum((request) => request.usage?.input_tokens),
          responderOutputTokens: sum((request) => request.usage?.output_tokens),
          responderReasoningTokens: sum((request) => request.usage?.reasoning_tokens),
        }
      : {}),
    ...(efforts.length ? { responderReasoningEffort: efforts.join(",") } : {}),
    writtenReplies: replies.length,
    writtenRepliesWithResponderText: replies.filter((reply) => reply.responderText).length,
  };
}
