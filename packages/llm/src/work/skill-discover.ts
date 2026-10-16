import { mkdir, readFile, rename } from "node:fs/promises";
import { join } from "node:path";
import type { RunEvidence } from "./skill-learn-evidence";
import {
  applyPlaybookOperations,
  collectAnswerNumbers,
  groundEvidence,
  leakedNumbers,
  readLearnedRules,
  readRejectedRules,
  renderLearnedRules,
  writeLearnedRules,
  type LearnedRule,
} from "./skill-learn-playbook";
import type { SkillLearnLlm } from "./skill-learn-propose";
import { scanLearnedText } from "./skill-learn-scan";
import { writeWorkSkillFiles, normalizeSkillName } from "./skill-files";
import { readLearnedOverlay, writeLearnedOverlay } from "./skill-overlay";
import { fingerprintSkillTree } from "./workspace";

export const MIN_DISCOVERY_SUPPORT = 3;
export const MIN_DISCOVERY_RUNS = 5;
export const MAX_LEARNED_SKILLS = 20;
export const MAX_SKILL_BODY_CHARS = 4000;
export const LEARNED_SKILL_IDLE_DAYS = 30;
export const SKILL_ARCHIVE_DIR = "skill-archive";
export const LEARNED_ORIGIN = "learned";

export type CatalogSkill = {
  name: string;
  description: string;
  learned: boolean;
};

export type SkillDiscoveryProposal =
  | { action: "none"; rationale: string }
  | {
      action: "extend";
      rationale: string;
      skillName: string;
      rule: string;
      evidenceRunIds: string[];
      evidenceQuotes: string[];
    }
  | {
      action: "create";
      rationale: string;
      skill: { name: string; description: string; body: string };
      evidenceRunIds: string[];
      evidenceQuotes: string[];
    };

export type SkillDiscoveryResult = {
  decision: "created" | "extended" | "skipped" | "rejected";
  reason: string;
  skillName?: string;
  evidenceRunIds: string[];
  rationale?: string;
  rules?: LearnedRule[];
  /** The new skill's description, or the rule added to an existing skill. */
  lesson?: string;
};

const PROPOSAL_SHAPE = `{"action": "none", "rationale": "why"}
or {"action": "extend", "rationale": "why", "skillName": "existing-skill", "rule": "one general rule", "evidenceRunIds": ["run-id", "run-id", "run-id"], "evidenceQuotes": ["exact text copied from a cited run"]}
or {"action": "create", "rationale": "why", "skill": {"name": "kebab-case-name", "description": "when to use this skill", "body": "markdown steps and checks"}, "evidenceRunIds": ["run-id", "run-id", "run-id"], "evidenceQuotes": ["exact text copied from a cited run"]}`;

export function buildSkillDiscoveryPrompt(input: {
  catalog: readonly CatalogSkill[];
  evidence: readonly RunEvidence[];
}): string {
  const catalog = input.catalog.length
    ? input.catalog.map((skill) => `- ${skill.name}: ${skill.description}`).join("\n")
    : "(none)";
  return [
    "You keep an organization's skill catalog small and useful.",
    `Study the settled runs below. They used no skill. Find one procedure that repeats in at least ${MIN_DISCOVERY_SUPPORT} runs and that a written method would make faster or more correct next time.`,
    "Prefer extending an existing skill with one rule when a skill in the catalog already covers the same kind of work.",
    "Create a new skill only when no catalog skill covers the procedure. A new skill describes a general method with steps and checks, for any inputs of that kind.",
    "Keep specific answer values, dates and totals out of the skill and the rule.",
    `Cite at least ${MIN_DISCOVERY_SUPPORT} runs and copy a short exact quote (12 or more characters) from each cited run.`,
    'Return {"action": "none"} when nothing repeats or the procedure is too simple to need a skill.',
    "Return only JSON in one of these shapes:",
    PROPOSAL_SHAPE,
    "skill catalog:",
    catalog,
    "settled runs:",
    input.evidence.map((run) => run.digest).join("\n\n").slice(0, 30000),
  ].join("\n");
}

export function parseSkillDiscoveryProposal(raw: string): SkillDiscoveryProposal | null {
  const start = raw.indexOf("{");
  const end = raw.lastIndexOf("}");
  if (start < 0 || end <= start) return null;
  let json: Record<string, unknown>;
  try {
    json = JSON.parse(raw.slice(start, end + 1)) as Record<string, unknown>;
  } catch {
    return null;
  }
  const rationale = typeof json.rationale === "string" ? json.rationale : "";
  const runIds = strings(json.evidenceRunIds);
  const quotes = strings(json.evidenceQuotes);
  if (json.action === "none") return { action: "none", rationale };
  if (json.action === "extend" && typeof json.skillName === "string" && typeof json.rule === "string") {
    return {
      action: "extend",
      rationale,
      skillName: json.skillName,
      rule: json.rule,
      evidenceRunIds: runIds,
      evidenceQuotes: quotes,
    };
  }
  const skill = json.skill as Record<string, unknown> | undefined;
  if (
    json.action === "create" &&
    skill &&
    typeof skill.name === "string" &&
    typeof skill.description === "string" &&
    typeof skill.body === "string"
  ) {
    return {
      action: "create",
      rationale,
      skill: { name: skill.name, description: skill.description, body: skill.body },
      evidenceRunIds: runIds,
      evidenceQuotes: quotes,
    };
  }
  return null;
}

export async function runSkillDiscovery(input: {
  orgRoot: string;
  catalog: readonly CatalogSkill[];
  evidence: readonly RunEvidence[];
  llm: SkillLearnLlm;
}): Promise<SkillDiscoveryResult> {
  const evidenceRunIds = input.evidence.map((run) => run.runId);
  if (input.evidence.length < MIN_DISCOVERY_RUNS) {
    return { decision: "skipped", reason: "too few settled runs", evidenceRunIds };
  }
  const proposal = parseSkillDiscoveryProposal(
    await input.llm(buildSkillDiscoveryPrompt(input)),
  );
  if (!proposal) return { decision: "skipped", reason: "no_proposal", evidenceRunIds };
  if (proposal.action === "none") {
    return { decision: "skipped", reason: "nothing repeats", evidenceRunIds, rationale: proposal.rationale };
  }

  const evidenceById = new Map(input.evidence.map((run) => [run.runId, run]));
  const grounding = groundEvidence(proposal, evidenceById);
  if (grounding.runIds.length < MIN_DISCOVERY_SUPPORT) {
    return {
      decision: "skipped",
      reason: `grounded in ${grounding.runIds.length} run(s); needs ${MIN_DISCOVERY_SUPPORT}`,
      evidenceRunIds,
      rationale: proposal.rationale,
    };
  }

  if (proposal.action === "extend") {
    return extendSkill({ ...input, proposal, groundedRunIds: grounding.runIds });
  }

  const name = normalizeSkillName(proposal.skill.name);
  const text = `${proposal.skill.description}\n${proposal.skill.body}`;
  const hits = scanLearnedText(text);
  if (hits.length > 0) {
    return {
      decision: "rejected",
      reason: hits.map((hit) => hit.detail).join("; "),
      evidenceRunIds,
      rationale: proposal.rationale,
    };
  }
  const leaked = leakedNumbers(text, collectAnswerNumbers(input.evidence));
  const problem = !name
    ? "invalid skill name"
    : input.catalog.some((skill) => skill.name === name)
      ? `skill ${name} already exists`
      : proposal.skill.body.length > MAX_SKILL_BODY_CHARS
        ? "skill body is too long"
        : leaked.length > 0
          ? `skill repeats answer values: ${leaked.join(", ")}`
          : input.catalog.filter((skill) => skill.learned).length >= MAX_LEARNED_SKILLS
            ? `org holds ${MAX_LEARNED_SKILLS} learned skills`
            : null;
  if (problem) {
    return { decision: "skipped", reason: problem, evidenceRunIds, rationale: proposal.rationale };
  }
  await writeWorkSkillFiles(join(input.orgRoot, "skills"), {
    name,
    description: proposal.skill.description,
    body: proposal.skill.body,
    metadata: { origin: LEARNED_ORIGIN, evidence: grounding.runIds.join(",") },
  });
  return {
    decision: "created",
    reason: "created a skill from repeated runs",
    skillName: name,
    evidenceRunIds: grounding.runIds,
    rationale: proposal.rationale,
    lesson: proposal.skill.description,
  };
}

async function extendSkill(input: {
  orgRoot: string;
  catalog: readonly CatalogSkill[];
  evidence: readonly RunEvidence[];
  proposal: Extract<SkillDiscoveryProposal, { action: "extend" }>;
  groundedRunIds: string[];
}): Promise<SkillDiscoveryResult> {
  const { proposal } = input;
  const evidenceRunIds = input.evidence.map((run) => run.runId);
  if (!input.catalog.some((skill) => skill.name === proposal.skillName)) {
    return { decision: "skipped", reason: `unknown skill ${proposal.skillName}`, evidenceRunIds };
  }
  const skillDir = join(input.orgRoot, "skills", proposal.skillName);
  const baseHash = await fingerprintSkillTree(skillDir);
  const existing = await readLearnedOverlay(input.orgRoot, proposal.skillName);
  if (existing && existing.baseHash !== baseHash) {
    return { decision: "skipped", reason: "existing overlay is stale", evidenceRunIds };
  }
  const update = applyPlaybookOperations({
    current: await readLearnedRules(input.orgRoot, proposal.skillName),
    operations: [
      {
        op: "add",
        text: proposal.rule,
        evidenceRunIds: input.groundedRunIds,
        evidenceQuotes: proposal.evidenceQuotes,
      },
    ],
    evidence: input.evidence,
    rejected: await readRejectedRules(input.orgRoot, proposal.skillName),
  });
  if (update.unsafe.length > 0) {
    return { decision: "rejected", reason: update.unsafe.join("; "), evidenceRunIds };
  }
  if (update.applied.length === 0) {
    return {
      decision: "skipped",
      reason: update.dropped[0]?.reason ?? "rule not applied",
      evidenceRunIds,
    };
  }
  await writeLearnedOverlay(input.orgRoot, {
    skillName: proposal.skillName,
    baseHash,
    status: "applied",
    body: renderLearnedRules(update.rules),
  });
  await writeLearnedRules(input.orgRoot, proposal.skillName, update.rules);
  return {
    decision: "extended",
    reason: "added a rule to an existing skill",
    skillName: proposal.skillName,
    evidenceRunIds: input.groundedRunIds,
    rationale: proposal.rationale,
    rules: update.rules,
    lesson: proposal.rule,
  };
}

/** Moves learned skills with no recent use out of the catalog. */
export async function archiveIdleLearnedSkills(input: {
  orgRoot: string;
  catalog: readonly CatalogSkill[];
  lastUsedAt: ReadonlyMap<string, Date>;
  createdAt: ReadonlyMap<string, Date>;
  now?: Date;
}): Promise<string[]> {
  const now = (input.now ?? new Date()).getTime();
  const cutoff = LEARNED_SKILL_IDLE_DAYS * 24 * 60 * 60 * 1000;
  const archived: string[] = [];
  for (const skill of input.catalog) {
    if (!skill.learned) continue;
    const last = input.lastUsedAt.get(skill.name) ?? input.createdAt.get(skill.name);
    if (!last || now - last.getTime() < cutoff) continue;
    const archiveRoot = join(input.orgRoot, SKILL_ARCHIVE_DIR);
    await mkdir(archiveRoot, { recursive: true });
    await rename(
      join(input.orgRoot, "skills", skill.name),
      join(archiveRoot, `${skill.name}-${new Date(now).toISOString().slice(0, 10)}`),
    );
    archived.push(skill.name);
  }
  return archived;
}

export async function isLearnedSkill(skillDir: string): Promise<boolean> {
  try {
    const markdown = await readFile(join(skillDir, "SKILL.md"), "utf8");
    const frontmatter = markdown.match(/^---\s*\n([\s\S]*?)\n---/)?.[1] ?? "";
    return new RegExp(`^\\s+origin:\\s*"?${LEARNED_ORIGIN}"?\\s*$`, "m").test(frontmatter);
  } catch {
    return false;
  }
}

function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}
