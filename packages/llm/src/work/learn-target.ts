import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { LearnTargetKind } from "@neko/db/jobs";
import type { LearnedRule } from "./skill-learn-playbook";
import { skillDescriptionDir, skillOverlayDir } from "./skill-overlay";
import { LEARNED_DIR, MAX_ORG_RULES, ORG_PLAYBOOK_FILE, SOURCE_NOTES_FILE } from "./learned-context";

export { CITE_RULES_INSTRUCTION } from "./skill-overlay";
export { LEARNED_DIR, MAX_ORG_RULES, ORG_PLAYBOOK_FILE, SOURCE_NOTES_FILE };

export type { LearnTargetKind };

export type LearnTarget = { kind: LearnTargetKind; id: string };

export const LEARN_TARGET_KINDS: readonly LearnTargetKind[] = [
  "skill",
  "source",
  "skill-description",
  "org",
];
export const ORG_TARGET_ID = "org";
/** Threads on this channel are run by or for the learner; they never count as evidence. */
export const LEARNING_THREAD_CHANNEL = "learning";
/** Tool names that read the customer data source, as a case-insensitive regular expression. */
export const GRAPHJIN_TOOL_PATTERN = "(graphjin|graphql|gj_catalog|query_catalog|saved_query)";

const MINUTE = 60 * 1000;

export type LearnTargetPolicy = {
  maxRules: number;
  minSupport: number;
  minIntervalMs: number;
};

export const LEARN_TARGET_POLICY: Record<LearnTargetKind, LearnTargetPolicy> = {
  skill: { maxRules: 12, minSupport: 2, minIntervalMs: 0 },
  source: { maxRules: 12, minSupport: 2, minIntervalMs: 0 },
  "skill-description": { maxRules: 1, minSupport: 3, minIntervalMs: 15 * MINUTE },
  org: { maxRules: MAX_ORG_RULES, minSupport: 2, minIntervalMs: 15 * MINUTE },
};

export function isLearnTargetKind(value: unknown): value is LearnTargetKind {
  return typeof value === "string" && (LEARN_TARGET_KINDS as readonly string[]).includes(value);
}

/** The event-log key; a skill target keeps its bare name so older rows and readers still match. */
export function learnTargetKey(target: LearnTarget): string {
  return target.kind === "skill" ? target.id : `${target.kind}:${target.id}`;
}

export function learnTargetDir(orgRoot: string, target: LearnTarget): string {
  switch (target.kind) {
    case "skill":
      return skillOverlayDir(orgRoot, target.id);
    case "skill-description":
      return skillDescriptionDir(orgRoot, target.id);
    case "source":
      return join(orgRoot, LEARNED_DIR, "sources", target.id);
    case "org":
      return join(orgRoot, LEARNED_DIR, "org");
  }
}

/** The config-VCS path a target's learned files live under. */
export function learnTargetVcsPath(target: LearnTarget): string {
  switch (target.kind) {
    case "skill":
    case "skill-description":
      return `skill-overlays/${target.id}`;
    case "source":
      return `${LEARNED_DIR}/sources/${target.id}`;
    case "org":
      return `${LEARNED_DIR}/org`;
  }
}

export function ruleKey(target: LearnTarget, ruleId: string): string {
  return `${target.kind}:${target.id}:${ruleId}`;
}

const RULE_KEY_RE = /\b(skill|source|org):([A-Za-z0-9._-]+):(L\d+)\b/g;

export function parseRuleKey(key: string): (LearnTarget & { ruleId: string }) | null {
  const match = /^(skill|source|org):([A-Za-z0-9._-]+):(L\d+)$/.exec(key);
  if (!match) return null;
  return { kind: match[1] as LearnTargetKind, id: match[2]!, ruleId: match[3]! };
}

/** Rule keys an agent cited in its own text. */
export function extractRuleCitations(text: string): string[] {
  return [...new Set([...text.matchAll(RULE_KEY_RE)].map((match) => match[0]))];
}


export function renderKeyedRules(target: LearnTarget, rules: readonly LearnedRule[]): string {
  return rules.map((rule) => `- [${ruleKey(target, rule.id)}] ${rule.text}`).join("\n");
}

export type LearnedNotes = {
  target: LearnTarget;
  status: "applied" | "disabled";
  body: string;
};

function notesFile(target: LearnTarget): string {
  return target.kind === "org" ? ORG_PLAYBOOK_FILE : SOURCE_NOTES_FILE;
}

export async function readLearnedNotes(
  orgRoot: string,
  target: LearnTarget,
): Promise<LearnedNotes | null> {
  try {
    const raw = await readFile(join(learnTargetDir(orgRoot, target), notesFile(target)), "utf8");
    const match = raw.match(/^---\s*\n([\s\S]*?)\n---\s*\n?([\s\S]*)$/);
    if (!match) return null;
    const status = /^status:\s*(\S+)\s*$/m.exec(match[1] ?? "")?.[1];
    if (status !== "applied" && status !== "disabled") return null;
    return { target, status, body: (match[2] ?? "").trim() };
  } catch {
    return null;
  }
}

export async function writeLearnedNotes(orgRoot: string, notes: LearnedNotes): Promise<void> {
  const dir = learnTargetDir(orgRoot, notes.target);
  await mkdir(dir, { recursive: true });
  await writeFile(
    join(dir, notesFile(notes.target)),
    [
      "---",
      `target_kind: ${notes.target.kind}`,
      `target_id: ${notes.target.id}`,
      `status: ${notes.status}`,
      "---",
      "",
      notes.body.trim(),
      "",
    ].join("\n"),
    "utf8",
  );
}
