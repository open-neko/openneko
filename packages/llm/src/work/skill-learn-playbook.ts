import { readFile, writeFile, mkdir } from "node:fs/promises";
import { join } from "node:path";
import type { RunEvidence } from "./skill-learn-evidence";
import { scanLearnedText } from "./skill-learn-scan";
import { skillOverlayDir } from "./skill-overlay";

export const RULES_FILE = "RULES.json";
export const REJECTED_FILE = "REJECTED.json";
export const MAX_RULES = 12;
export const MAX_RULE_CHARS = 300;
export const MIN_QUOTE_CHARS = 12;
export const MIN_RULE_SUPPORT = 2;

export type LearnedRule = {
  id: string;
  text: string;
  evidenceRunIds: string[];
  evidenceQuotes: string[];
};

export type PlaybookOperation = {
  op: "add" | "update" | "remove";
  ruleId?: string;
  text?: string;
  evidenceRunIds?: string[];
  evidenceQuotes?: string[];
};

export type DroppedOperation = { operation: PlaybookOperation; reason: string };

export type PlaybookApplyResult = {
  rules: LearnedRule[];
  applied: PlaybookOperation[];
  dropped: DroppedOperation[];
  unsafe: string[];
  unsafeOperations: DroppedOperation[];
};

export async function readLearnedRules(
  orgRoot: string,
  skillName: string,
): Promise<LearnedRule[]> {
  return readRulesAt(skillOverlayDir(orgRoot, skillName));
}

export async function writeLearnedRules(
  orgRoot: string,
  skillName: string,
  rules: readonly LearnedRule[],
): Promise<void> {
  await writeRulesAt(skillOverlayDir(orgRoot, skillName), rules);
}

export async function readRejectedRules(
  orgRoot: string,
  skillName: string,
): Promise<string[]> {
  return readRejectedAt(skillOverlayDir(orgRoot, skillName));
}

export async function rejectLearnedRules(
  orgRoot: string,
  skillName: string,
  rules: readonly LearnedRule[],
): Promise<void> {
  await rejectTextsAt(skillOverlayDir(orgRoot, skillName), rules.map((rule) => rule.text));
  await writeLearnedRules(orgRoot, skillName, []);
}

export async function readRulesAt(dir: string): Promise<LearnedRule[]> {
  try {
    const raw = JSON.parse(await readFile(join(dir, RULES_FILE), "utf8")) as unknown;
    if (!Array.isArray(raw)) return [];
    return raw.filter(isRule);
  } catch {
    return [];
  }
}

export async function writeRulesAt(dir: string, rules: readonly LearnedRule[]): Promise<void> {
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, RULES_FILE), `${JSON.stringify(rules, null, 2)}\n`, "utf8");
}

export async function readRejectedAt(dir: string): Promise<string[]> {
  try {
    const raw = JSON.parse(await readFile(join(dir, REJECTED_FILE), "utf8")) as unknown;
    return Array.isArray(raw) ? raw.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

export async function rejectTextsAt(dir: string, texts: readonly string[]): Promise<void> {
  const rejected = new Set(await readRejectedAt(dir));
  for (const text of texts) rejected.add(normalize(text));
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, REJECTED_FILE), `${JSON.stringify([...rejected], null, 2)}\n`, "utf8");
}

export function renderLearnedRules(rules: readonly LearnedRule[]): string {
  return rules.map((rule) => `- ${rule.text}`).join("\n");
}

export function applyPlaybookOperations(input: {
  current: readonly LearnedRule[];
  operations: readonly PlaybookOperation[];
  evidence: readonly RunEvidence[];
  rejected?: readonly string[];
  maxRules?: number;
  minSupport?: number;
  /** A target-specific reason to drop a rule, or null to keep it. */
  check?: (text: string) => string | null;
}): PlaybookApplyResult {
  const maxRules = input.maxRules ?? MAX_RULES;
  const minSupport = input.minSupport ?? MIN_RULE_SUPPORT;
  const rejected = new Set(input.rejected ?? []);
  const rules = input.current.map((rule) => ({ ...rule }));
  const evidenceById = new Map(input.evidence.map((run) => [run.runId, run]));
  const answerNumbers = collectAnswerNumbers(input.evidence);
  const applied: PlaybookOperation[] = [];
  const dropped: DroppedOperation[] = [];
  const unsafe: string[] = [];
  const unsafeOperations: DroppedOperation[] = [];
  let nextId = nextRuleNumber(rules);

  for (const operation of input.operations) {
    if (operation.op === "remove") {
      const index = rules.findIndex((rule) => rule.id === operation.ruleId);
      if (index < 0) {
        dropped.push({ operation, reason: "unknown rule id" });
        continue;
      }
      rules.splice(index, 1);
      applied.push(operation);
      continue;
    }

    const text = (operation.text ?? "").replace(/\s+/g, " ").trim();
    if (!text) {
      dropped.push({ operation, reason: "empty rule" });
      continue;
    }
    const hits = scanLearnedText(text);
    if (hits.length > 0) {
      const detail = hits.map((hit) => hit.detail).join("; ");
      unsafe.push(`${text.slice(0, 80)}: ${detail}`);
      unsafeOperations.push({ operation, reason: `unsafe: ${detail}` });
      continue;
    }
    if (rejected.has(normalize(text))) {
      dropped.push({ operation, reason: "rule was rolled back earlier" });
      continue;
    }
    if (text.length > MAX_RULE_CHARS) {
      dropped.push({ operation, reason: "rule is too long" });
      continue;
    }
    const leaked = leakedNumbers(text, answerNumbers);
    if (leaked.length > 0) {
      dropped.push({ operation, reason: `rule repeats answer values: ${leaked.join(", ")}` });
      continue;
    }
    const targetReason = input.check?.(text);
    if (targetReason) {
      dropped.push({ operation, reason: targetReason });
      continue;
    }
    const grounding = groundEvidence(operation, evidenceById);
    if (grounding.runIds.length < minSupport) {
      dropped.push({
        operation,
        reason: `grounded in ${grounding.runIds.length} run(s); needs ${minSupport}`,
      });
      continue;
    }

    if (!ruleIsSpecific(text, grounding.runIds.map((id) => evidenceById.get(id)!))) {
      dropped.push({ operation, reason: "rule is not specific" });
      continue;
    }

    if (operation.op === "update") {
      const rule = rules.find((candidate) => candidate.id === operation.ruleId);
      if (!rule) {
        dropped.push({ operation, reason: "unknown rule id" });
        continue;
      }
      rule.text = text;
      rule.evidenceRunIds = unique([...rule.evidenceRunIds, ...grounding.runIds]);
      rule.evidenceQuotes = unique([...rule.evidenceQuotes, ...grounding.quotes]);
      applied.push(operation);
      continue;
    }

    if (rules.some((rule) => normalize(rule.text) === normalize(text))) {
      dropped.push({ operation, reason: "duplicate rule" });
      continue;
    }
    if (rules.length >= maxRules) {
      dropped.push({ operation, reason: `playbook holds ${maxRules} rules` });
      continue;
    }
    rules.push({
      id: `L${nextId++}`,
      text,
      evidenceRunIds: grounding.runIds,
      evidenceQuotes: grounding.quotes,
    });
    applied.push(operation);
  }

  return { rules, applied, dropped, unsafe, unsafeOperations };
}

export function groundEvidence(
  operation: Pick<PlaybookOperation, "evidenceRunIds" | "evidenceQuotes">,
  evidenceById: Map<string, RunEvidence>,
): { runIds: string[]; quotes: string[] } {
  const quotes = (operation.evidenceQuotes ?? [])
    .map((quote) => quote.trim())
    .filter((quote) => quote.length >= MIN_QUOTE_CHARS);
  const runIds: string[] = [];
  const grounded: string[] = [];
  for (const runId of unique(operation.evidenceRunIds ?? [])) {
    const run = evidenceById.get(runId);
    if (!run) continue;
    const digest = quoteText(run.grounding ?? run.digest);
    const match = quotes.find((quote) => digest.includes(quoteText(quote)));
    if (!match) continue;
    runIds.push(runId);
    grounded.push(match);
  }
  return { runIds, quotes: unique(grounded) };
}

/** Digest text as a model quotes it: JSON escapes read as the characters they stand for. */
function quoteText(text: string): string {
  return normalize(text.replace(/\\[nrt]/g, " ").replace(/\\(["'\\/])/g, "$1"));
}

const STEP_RE = /^step \d+: [^(\n]*\((.*?)\) -> (.*)$/gm;
const IDENTIFIER_RE = /[a-z][a-z0-9]*(?:[_.][a-z0-9]+)+/gi;
const INPUT_WORD_RE = /\b[a-z][a-z0-9]{5,}\b/g;
const GENERIC_TERMS = new Set(
  "ensure robust handle handling verify carefully always should before result results return returns select variables mutation fragment string number object boolean values".split(" "),
);

/** Table, column, field and function names the runs' tool inputs and results used. */
export function concreteRunTerms(runs: readonly RunEvidence[]): Set<string> {
  const terms = new Set<string>();
  for (const run of runs) {
    for (const [, input = "", result = ""] of (run.grounding ?? run.digest).matchAll(STEP_RE)) {
      for (const match of `${input}\n${result}`.matchAll(IDENTIFIER_RE)) terms.add(match[0].toLowerCase());
      for (const match of input.matchAll(INPUT_WORD_RE)) {
        if (!GENERIC_TERMS.has(match[0])) terms.add(match[0]);
      }
    }
  }
  return terms;
}

/** A rule must name something concrete the cited runs touched. */
export function ruleIsSpecific(text: string, runs: readonly RunEvidence[]): boolean {
  const terms = concreteRunTerms(runs);
  return (text.toLowerCase().match(IDENTIFIER_RE) ?? []).some((term) => terms.has(term)) ||
    (text.toLowerCase().match(/[a-z][a-z0-9]*/g) ?? []).some((word) => word.length >= 6 && terms.has(word));
}

const NUMBER_RE = /\d[\d,]*(?:\.\d+)?/g;

export function collectAnswerNumbers(evidence: readonly RunEvidence[]): Set<string> {
  const numbers = new Set<string>();
  for (const run of evidence) {
    for (const match of run.answer.matchAll(NUMBER_RE)) {
      const value = match[0].replace(/,/g, "");
      if (value.replace(".", "").length >= 3) numbers.add(value);
    }
  }
  return numbers;
}

export function leakedNumbers(text: string, answerNumbers: Set<string>): string[] {
  const leaked: string[] = [];
  for (const match of text.matchAll(NUMBER_RE)) {
    const value = match[0].replace(/,/g, "");
    if (answerNumbers.has(value)) leaked.push(value);
  }
  return leaked;
}

function nextRuleNumber(rules: readonly LearnedRule[]): number {
  let max = 0;
  for (const rule of rules) {
    const n = Number(rule.id.replace(/^L/, ""));
    if (Number.isFinite(n) && n > max) max = n;
  }
  return max + 1;
}

export function normalizeRuleText(text: string): string {
  return normalize(text);
}

function normalize(text: string): string {
  return text.toLowerCase().replace(/\s+/g, " ").trim();
}

function unique<T>(values: readonly T[]): T[] {
  return [...new Set(values)];
}

function isRule(value: unknown): value is LearnedRule {
  if (!value || typeof value !== "object") return false;
  const rule = value as Record<string, unknown>;
  return (
    typeof rule.id === "string" &&
    typeof rule.text === "string" &&
    Array.isArray(rule.evidenceRunIds) &&
    Array.isArray(rule.evidenceQuotes)
  );
}
