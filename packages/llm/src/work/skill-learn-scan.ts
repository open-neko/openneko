const SECRET_RE =
  /(api[_-]?key|secret|password|bearer\s+[a-z0-9._-]+|sk-[a-z0-9]{16,})/i;
const EMAIL_RE = /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/i;
const FRONTMATTER_RE = /^---\s*$/m;
const TOOL_PERM_RE = /\ballowed-tools\s*:/i;
const BOUNDARY_RE = /^\s*Boundary\s*:/m;
const HELD_BACK =
  "(?:(?:any|specific|exact|concrete|precise|the|actual|numeric|explicit|individual)\\s+){0,2}" +
  "(?:numbers?|figures?|totals?|amounts?|values?|counts?|numerals?|dates?|date ranges?|time windows?|windows?)\\b";
const IN_ANSWER = "\\s+(?:in|from)\\s+(?:the\\s+|your\\s+)?(?:answers?|responses?|repl(?:y|ies)|summar(?:y|ies))";
/** Blanket guidance to hold back figures or dates; scoped rules such as "never report totals from truncated rows" pass. */
const EVASION_RE = new RegExp(
  "\\b(?:avoid|do not|don't|never|refrain from)\\s+" +
    "(?:(?:stat|mention|giv|includ|report|cit|quot|show|list|provid|writ|spell)\\w*\\s+)?" +
    HELD_BACK +
    `(?=\\s*(?:$|[.,;:!)])|${IN_ANSWER})` +
    `|\\b(?:omit|leave out|withhold)\\s+${HELD_BACK}${IN_ANSWER}` +
    `|\\b(?:answers?|responses?|replies)\\s+free of\\s+${HELD_BACK}` +
    `|\\bbe vague about\\s+${HELD_BACK}`,
  "i",
);

export type LearnedScanHit = {
  code: "secret" | "pii" | "frontmatter" | "tools" | "boundary" | "evasion";
  detail: string;
};

export function scanLearnedText(text: string): LearnedScanHit[] {
  const hits: LearnedScanHit[] = [];
  if (SECRET_RE.test(text)) {
    hits.push({ code: "secret", detail: "candidate looks like a secret" });
  }
  if (EMAIL_RE.test(text)) {
    hits.push({ code: "pii", detail: "candidate contains an email address" });
  }
  if (FRONTMATTER_RE.test(text.trimStart()) || /^\s*name\s*:/m.test(text)) {
    hits.push({
      code: "frontmatter",
      detail: "candidate tries to set skill frontmatter",
    });
  }
  if (TOOL_PERM_RE.test(text)) {
    hits.push({ code: "tools", detail: "candidate edits tool permissions" });
  }
  if (BOUNDARY_RE.test(text)) {
    hits.push({
      code: "boundary",
      detail: "candidate edits a safety boundary",
    });
  }
  if (EVASION_RE.test(text)) {
    hits.push({
      code: "evasion",
      detail: "candidate tells the agent to hold back figures or dates",
    });
  }
  return hits;
}

export function assertAdditiveLearnedBody(text: string): string | null {
  const trimmed = text.trim();
  if (!trimmed) return "learned body is empty";
  if (trimmed.length > 8000) return "learned body is too long";
  const hits = scanLearnedText(trimmed);
  if (hits.length > 0) return hits.map((hit) => hit.detail).join("; ");
  return null;
}

export const MAX_DESCRIPTION_CHARS = 400;
const MIN_DESCRIPTION_CHARS = 20;
const AUTHORITY_RE =
  /\b(tools?|permissions?|allowed|approv\w*|sudo|shell|bash|credentials?|tokens?|system prompt|instructions?|ignore|override|bypass|policy|polic(y|ies)|budget|limit)\b/i;
const ADDRESSES_AGENT_RE = /\b(you must|you should|always call|never call|do not|don't|must not)\b/i;
const UI_RE = /\b(click|button|screen|sidebar|menu|tab|dialog|page|ui|settings page|admin panel)\b/i;

/** A learned description must stay a one-line routing description. */
export function checkLearnedDescription(text: string): string | null {
  const description = text.trim();
  if (description.length < MIN_DESCRIPTION_CHARS) return "description is too short";
  if (description.length > MAX_DESCRIPTION_CHARS) return "description is too long";
  if (/[\r\n"\\`]/.test(description)) return "description must be one line without quotes or code";
  const hits = scanLearnedText(description);
  if (hits.length > 0) return hits.map((hit) => hit.detail).join("; ");
  if (AUTHORITY_RE.test(description)) return "description mentions tools, permissions or policy";
  if (ADDRESSES_AGENT_RE.test(description)) return "description gives the agent instructions";
  return null;
}

/** Org playbook rules describe work habits, never app or UI internals. */
export function checkOrgRule(text: string): string | null {
  return UI_RE.test(text) ? "rule mentions app or UI internals" : null;
}
