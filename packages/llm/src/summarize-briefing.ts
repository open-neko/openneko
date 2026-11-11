import { generate, hostProgram } from "./inference";

const DESCRIPTION = `You write a one- to three-sentence operator briefing. Tone: calm, observational, plain. No greetings, no time-of-day framing, no morning/evening references, no exclamation points. State what's waiting, what's worth a look, and what was quiet — only the parts that have content. If nothing has happened, say so directly. Maximum 60 words.`;

const WRITER = hostProgram(
  `pendingApprovals:number "queued approvals awaiting decision", actFindings:number "findings marked act (need attention)", watchFindings:number "findings marked watch (worth a look)", goodRuns:number "workflows that ran cleanly in the window", pinnedCount:number "items the operator pinned to the briefing", windowHours:number "lookback window in hours", topApprovalTitle?:string "title of the most pressing approval, may be empty", topActTitle?:string "title of the most pressing act finding, may be empty" -> briefing:string "1-3 sentence operator briefing, plain calm tone, max 60 words"`,
  DESCRIPTION,
);

export type BriefingSummaryInput = {
  pendingApprovals: number;
  actFindings: number;
  watchFindings: number;
  goodRuns: number;
  pinnedCount: number;
  windowHours: number;
  topApprovalTitle?: string | null;
  topActTitle?: string | null;
};

export async function summarizeBriefing(
  input: BriefingSummaryInput,
  orgId?: string,
): Promise<string> {
  const { outputs: result } = await generate(orgId, WRITER, {
    pendingApprovals: input.pendingApprovals,
    actFindings: input.actFindings,
    watchFindings: input.watchFindings,
    goodRuns: input.goodRuns,
    pinnedCount: input.pinnedCount,
    windowHours: input.windowHours,
    topApprovalTitle: input.topApprovalTitle ?? "",
    topActTitle: input.topActTitle ?? "",
  }, { task: "briefing_summary" });
  const text = String(result.briefing ?? "").trim();
  return text;
}
