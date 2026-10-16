import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { SkillLearnProposal } from "./skill-learn";
import { GRAPHJIN_TOOL_PATTERN } from "./learn-target";
import type { RunEvidence } from "./skill-learn-evidence";
import type { LearnedRule, PlaybookOperation } from "./skill-learn-playbook";
import { stripLearnedSection } from "./skill-overlay";

export type SkillLearnLlm = (prompt: string) => Promise<string>;

const MAX_SKILL_CHARS = 6000;
const MAX_EVIDENCE_CHARS = 24000;
const MAX_GUIDE_CHARS = 2500;
const DATA_STEP_RE = new RegExp(`^step \\d+: [^(]*${GRAPHJIN_TOOL_PATTERN}`, "i");
const CONCRETE_LINE = "Name the table, column, join or calculation the rule is about, as the runs' queries and results spell it.";

const UPDATE_SHAPE = `{
  "rationale": "what the runs show, citing run ids",
  "operations": [
    {"op": "add", "text": "one general rule", "evidenceRunIds": ["run-id", "run-id"], "evidenceQuotes": ["exact text copied from each cited run"]},
    {"op": "update", "ruleId": "L1", "text": "revised rule", "evidenceRunIds": ["run-id"], "evidenceQuotes": ["exact text"]},
    {"op": "remove", "ruleId": "L2"}
  ]
}`;

const WRONG_RUN_LINES = [
  "An operator follow-up that disputes or corrects an answer means that run was wrong, even when its outcome says completed.",
  "A \"check failed\" line or a \"recompute disagreed\" line also means that run was wrong, even when its outcome says completed. Learn the method that gets those figures and dates right, and keep answers stating the figures and dates they found.",
];

export function parseSkillLearnProposal(
  raw: string,
  allowedRunIds: readonly string[],
): SkillLearnProposal | null {
  const json = extractJsonObject(raw);
  if (!json) return null;
  const rationale = typeof json.rationale === "string" ? json.rationale.trim() : "";
  if (!Array.isArray(json.operations)) return null;
  const allowed = new Set(allowedRunIds);
  const operations: PlaybookOperation[] = [];
  for (const item of json.operations) {
    if (!item || typeof item !== "object") continue;
    const op = (item as Record<string, unknown>).op;
    if (op !== "add" && op !== "update" && op !== "remove") continue;
    const record = item as Record<string, unknown>;
    operations.push({
      op,
      ...(typeof record.ruleId === "string" ? { ruleId: record.ruleId } : {}),
      ...(typeof record.text === "string" ? { text: record.text } : {}),
      evidenceRunIds: strings(record.evidenceRunIds).filter((id) => allowed.has(id)),
      evidenceQuotes: strings(record.evidenceQuotes),
    });
  }
  if (operations.length === 0) return null;
  return { rationale, operations };
}

export function buildSkillLearnPrompt(input: {
  skillName: string;
  skillMarkdown: string;
  rules: readonly LearnedRule[];
  evidence: readonly RunEvidence[];
}): string {
  const rules = input.rules.length
    ? input.rules.map((rule) => `${rule.id}: ${rule.text}`).join("\n")
    : "(none)";
  return [
    "You maintain a short playbook of learned rules for one agent skill.",
    "Study the settled runs below. Find procedures that made runs succeed and mistakes that made runs fail or need correction.",
    "Write general rules that help the next run with different inputs. A rule states a method, a check or a pitfall.",
    CONCRETE_LINE,
    ...WRONG_RUN_LINES,
    "Each added or updated rule must cite at least two runs and copy a short exact quote (12 or more characters) from each cited run.",
    "Keep rules free of specific answer values, dates and totals from these runs.",
    "Remove a rule when the runs show it is wrong. Return no operations when the runs teach nothing new.",
    "Return only JSON matching this shape:",
    UPDATE_SHAPE,
    `skill: ${input.skillName}`,
    "skill guide:",
    stripLearnedSection(input.skillMarkdown).slice(0, MAX_SKILL_CHARS),
    "current rules:",
    rules,
    "settled runs:",
    evidenceBlock(input.evidence),
  ].join("\n");
}

/**
 * Every run gets an equal share of the budget. A long digest keeps its
 * request, answer and verdict lines, then data query steps, then other steps,
 * so each run stays quotable.
 */
export function evidenceBlock(evidence: readonly RunEvidence[], budget = MAX_EVIDENCE_CHARS): string {
  const share = Math.floor(budget / Math.max(1, evidence.length));
  return evidence.map((run) => compactDigest(run.digest, share)).join("\n\n");
}

function compactDigest(digest: string, budget: number): string {
  if (digest.length <= budget) return digest;
  const lines = digest.split("\n");
  const isStep = (line: string) => /^step \d+: /.test(line);
  const rank = (line: string) => (!isStep(line) ? 0 : DATA_STEP_RE.test(line) || /-> error: /.test(line) ? 1 : 2);
  const keep = new Set<number>();
  let used = 0;
  for (const tier of [0, 1, 2]) {
    lines.forEach((line, index) => {
      if (rank(line) !== tier || used + line.length + 1 > budget) return;
      keep.add(index);
      used += line.length + 1;
    });
  }
  return lines.filter((_, index) => keep.has(index)).join("\n");
}

const TARGET_INTRO: Record<"source" | "org", string[]> = {
  source: [
    "You maintain short data notes for one data source that an agent queries.",
    "Study the settled runs below. Find what made queries on this source right or wrong.",
    "Write notes about the data: join paths that work, what columns mean, how to anchor dates and windows, and fan-out traps that inflate totals.",
    "The data query steps show each query and its result. Compare the query of a wrong run with the query of a right run to find the data mistake, and quote the query text that shows it.",
  ],
  org: [
    "You maintain a short playbook of work habits for one organization. Every run in the organization reads it.",
    "Study the settled runs below. Find habits that held across different requests and made answers right, and habits whose absence made answers wrong.",
    "Write cross-cutting habits, for example: anchor a trailing window on the latest order date the data holds. Write each rule as a positive instruction about the work or the data. Describe the work only; leave out product screens and controls.",
  ],
};

export function buildTargetLearnPrompt(input: {
  kind: "source" | "org";
  label: string;
  rules: readonly LearnedRule[];
  evidence: readonly RunEvidence[];
  maxRules: number;
}): string {
  const rules = input.rules.length
    ? input.rules.map((rule) => `${rule.id}: ${rule.text}`).join("\n")
    : "(none)";
  return [
    ...TARGET_INTRO[input.kind],
    CONCRETE_LINE,
    ...WRONG_RUN_LINES,
    "Each added or updated rule must cite at least two runs and copy a short exact quote (12 or more characters) from each cited run.",
    "Keep rules free of specific answer values, dates and totals from these runs.",
    `Keep at most ${input.maxRules} rules. Remove a rule when the runs show it is wrong. Return no operations when the runs teach nothing new.`,
    "Return only JSON matching this shape:",
    UPDATE_SHAPE,
    `${input.kind === "source" ? "data source" : "organization"}: ${input.label}`,
    "current rules:",
    rules,
    "settled runs:",
    evidenceBlock(input.evidence),
  ].join("\n");
}

export type DescriptionProposal = {
  rationale: string;
  /** The skill whose description changes; absent means the queued skill. */
  skill?: string;
  description: string;
  evidenceRunIds: string[];
  evidenceQuotes: string[];
};

export type DescriptionCandidate = { name: string; description: string; guide: string };

const DESCRIPTION_SHAPE = `{
  "rationale": "which requests the chosen guide serves, citing run ids",
  "skill": "the name of the skill whose guide serves these requests",
  "description": "the improved one-line description of that skill",
  "evidenceRunIds": ["run-id", "run-id", "run-id"],
  "evidenceQuotes": ["exact text copied from each cited run"]
}`;

export function buildDescriptionLearnPrompt(input: {
  skillName: string;
  candidates: readonly DescriptionCandidate[];
  evidence: readonly RunEvidence[];
  maxChars: number;
}): string {
  return [
    "You maintain the descriptions of agent skills. The agent reads skill descriptions to choose which skill guide to load for a request.",
    `The settled runs below loaded the skill ${input.skillName}, loaded several skills, or were corrected or failed verification after a skill was loaded.`,
    "Read the candidate skill guides below. Pick the one skill whose guide serves the requests in these runs.",
    "Write an improved description for that skill only. Keep what its current description says, and add the request terms its guide supports, so requests like these reach it.",
    "Use only words that appear in that skill's guide or current description. State what the skill does, in positive terms.",
    `Write one plain sentence or two, at most ${input.maxChars} characters, without quotes. Describe the requests and the data; leave out instructions to the agent.`,
    "Cite at least three runs and copy a short exact quote (12 or more characters) from each cited run.",
    'Return {"skill": null, "description": null} when no candidate guide serves these requests, or when the current descriptions already route them well.',
    "Return only JSON matching this shape:",
    DESCRIPTION_SHAPE,
    "candidate skills:",
    input.candidates
      .map((skill) =>
        [
          `## skill: ${skill.name}`,
          `current description: ${skill.description}`,
          "guide:",
          skill.guide.slice(0, MAX_GUIDE_CHARS),
        ].join("\n"),
      )
      .join("\n\n"),
    "settled runs:",
    evidenceBlock(input.evidence),
  ].join("\n");
}

export function parseDescriptionProposal(
  raw: string,
  allowedRunIds: readonly string[],
): DescriptionProposal | null {
  const json = extractJsonObject(raw);
  if (!json || typeof json.description !== "string") return null;
  const description = json.description.replace(/\s+/g, " ").trim();
  if (!description) return null;
  const allowed = new Set(allowedRunIds);
  return {
    rationale: typeof json.rationale === "string" ? json.rationale.trim() : "",
    ...(typeof json.skill === "string" && json.skill.trim() ? { skill: json.skill.trim() } : {}),
    description,
    evidenceRunIds: strings(json.evidenceRunIds).filter((id) => allowed.has(id)),
    evidenceQuotes: strings(json.evidenceQuotes),
  };
}

export async function callLearnLlm(
  orgId: string,
  prompt: string,
  llm?: SkillLearnLlm,
): Promise<string> {
  return (llm ?? (await defaultSkillLearnLlm(orgId)))(prompt);
}

export async function proposeSkillLearn(input: {
  orgId: string;
  orgRoot: string;
  skillName: string;
  rules: readonly LearnedRule[];
  evidence: readonly RunEvidence[];
  llm?: SkillLearnLlm;
}): Promise<SkillLearnProposal | null> {
  const skillMarkdown = await readFile(
    join(input.orgRoot, "skills", input.skillName, "SKILL.md"),
    "utf8",
  ).catch(() => "");
  const prompt = buildSkillLearnPrompt({
    skillName: input.skillName,
    skillMarkdown,
    rules: input.rules,
    evidence: input.evidence,
  });
  const llm = input.llm ?? (await defaultSkillLearnLlm(input.orgId));
  const raw = await llm(prompt);
  return parseSkillLearnProposal(
    raw,
    input.evidence.map((run) => run.runId),
  );
}

async function defaultSkillLearnLlm(orgId: string): Promise<SkillLearnLlm> {
  const [{ ax }, { buildLlm }, { recordLearnerSpend }] = await Promise.all([
    import("@ax-llm/ax"),
    import("../llm"),
    import("./learn-spend"),
  ]);
  const llm = await buildLlm(orgId);
  const generator = ax(
    `learningPrompt:string "instructions, skill guide, current rules and settled run digests" -> playbookUpdate:string "a JSON object with rationale and operations"`,
    {
      description:
        "You update a playbook of learned skill rules from run evidence. Reply with JSON only. No tools.",
    },
  );
  return async (prompt: string) => {
    try {
      const result = await generator.forward(llm, { learningPrompt: prompt });
      return String(result.playbookUpdate ?? "");
    } finally {
      await recordLearnerSpend(orgId, generator, "skill-learn");
    }
  };
}

function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function extractJsonObject(raw: string): Record<string, unknown> | null {
  const start = raw.indexOf("{");
  const end = raw.lastIndexOf("}");
  if (start < 0 || end <= start) return null;
  try {
    const parsed = JSON.parse(raw.slice(start, end + 1)) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
    return parsed as Record<string, unknown>;
  } catch {
    return null;
  }
}
