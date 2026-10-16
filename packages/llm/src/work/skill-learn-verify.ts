import { createHash } from "node:crypto";
import type { AgentEvent } from "../agent-backend";
import { GRAPHJIN_TOOL_PATTERN } from "./learn-target";

export type CheckVerdict = "pass" | "fail" | "unknown";
export type RecomputeVerdict = "agree" | "disagree" | "unknown";
export type AnswerCheckId = "ungrounded_number" | "ungrounded_date" | "truncated_data" | "answer_after_failure";
export type AnswerCheck = { id: AnswerCheckId; verdict: CheckVerdict; detail: string };

/** A verification row as the learner reads it. */
export type RunVerification = {
  kind: "check" | "recompute";
  checkId: string;
  verdict: CheckVerdict | RecomputeVerdict | "pending";
  detail: string;
};

export const DEFAULT_VERIFY_RATE = 0.1;
export const DEFAULT_RECOMPUTE_TOLERANCE = 0.01;
/** A sampled run settles once its recomputation finishes or after this long. */
export const DEFAULT_VERIFY_TIMEOUT_MS = 10 * 60 * 1000;

const DATA_TOOL_RE = new RegExp(GRAPHJIN_TOOL_PATTERN, "i");
const DAY_MS = 24 * 60 * 60 * 1000;
const MAX_POOL = 400;

export function isDataTool(name: string): boolean {
  return DATA_TOOL_RE.test(name);
}

export function verifyRate(env: NodeJS.ProcessEnv = process.env): number {
  const raw = env.OPENNEKO_LEARN_VERIFY_RATE;
  const value = raw === undefined || raw.trim() === "" ? NaN : Number(raw);
  return Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : DEFAULT_VERIFY_RATE;
}

/** A stable per-run draw, so a retried job makes the same choice. */
export function sampledForVerify(runId: string, rate: number): boolean {
  if (rate >= 1) return true;
  if (rate <= 0) return false;
  const draw = parseInt(createHash("sha256").update(runId).digest("hex").slice(0, 8), 16) / 0x100000000;
  return draw < rate;
}

export function isVerifiedWrong(rows: readonly RunVerification[]): boolean {
  return rows.some(
    (row) => (row.kind === "check" && row.verdict === "fail") || (row.kind === "recompute" && row.verdict === "disagree"),
  );
}

/** Digest lines for the learner: failed checks and settled recomputations. */
export function verificationLines(rows: readonly RunVerification[]): string[] {
  const lines: string[] = [];
  for (const row of rows) {
    if (row.kind === "check" && row.verdict === "fail") lines.push(`check failed: ${row.detail}`);
    if (row.kind === "recompute" && row.verdict !== "pending") {
      const word = row.verdict === "agree" ? "agreed" : row.verdict === "disagree" ? "disagreed" : "was inconclusive";
      lines.push(`recompute ${word}: ${row.detail}`);
    }
  }
  return lines;
}

// ---------- numbers ----------

export type AnswerNumber = {
  text: string;
  value: number;
  tolerance: number;
  percent: boolean;
  index: number;
};

const SCALE: Record<string, number> = {
  k: 1e3,
  thousand: 1e3,
  m: 1e6,
  mn: 1e6,
  million: 1e6,
  b: 1e9,
  bn: 1e9,
  billion: 1e9,
};

const MONTHS = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];
const MONTH_RE = "(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)";
const NUMBER_TOKEN_RE =
  /(?<![A-Za-z0-9_#/.:-])([$€£¥₹]\s?)?(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?(\s?(?:%|percent\b))?(\s?(?:k|K|M|B|bn|mn|million|billion|thousand)\b)?(?![A-Za-z0-9_/:]|\.\d)/g;
const TIME_UNIT_RE = /^\s?-?(?:days?|weeks?|months?|years?|hours?|minutes?|quarters?)\b/i;
const BOUND_RE = /(?:\b(?:over|more than|less than|under|at least|at most|up to|nearly|almost|above|below)|[<>≤≥])\s*$/i;

/** Dates and slash dates blanked out, so their parts never read as figures. */
function withoutDates(text: string): string {
  const blank = (match: string) => " ".repeat(match.length);
  return text
    .replace(/\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b/g, blank)
    .replace(/\b\d{4}-\d{2}\b/g, blank)
    .replace(/\b\d{1,2}\/\d{1,2}\/\d{2,4}\b/g, blank)
    .replace(new RegExp(`\\b${MONTH_RE}\\.?\\s+\\d{1,2}(?:st|nd|rd|th)?,?\\s+\\d{4}\\b`, "gi"), blank)
    .replace(new RegExp(`\\b\\d{1,2}(?:st|nd|rd|th)?\\s+${MONTH_RE}\\.?,?\\s+\\d{4}\\b`, "gi"), blank)
    .replace(new RegExp(`\\b${MONTH_RE}\\.?\\s+\\d{4}\\b`, "gi"), blank)
    .replace(/\bQ[1-4]\s+\d{4}\b/gi, blank)
    .replace(/\b\d{1,2}:\d{2}(?::\d{2})?\b/g, blank);
}

/** The figures an answer states, without years, small ranks, durations, bounds or identifiers. */
export function significantNumbers(text: string): AnswerNumber[] {
  const clean = withoutDates(text);
  const out: AnswerNumber[] = [];
  for (const match of clean.matchAll(NUMBER_TOKEN_RE)) {
    const [whole, currency, digits, decimals, percentRaw, scaleRaw] = match;
    const index = match.index ?? 0;
    const after = clean.slice(index + whole.length);
    const before = clean.slice(Math.max(0, index - 16), index);
    const percent = Boolean(percentRaw);
    const scaleKey = (scaleRaw ?? "").trim().toLowerCase();
    const scale = scaleKey ? SCALE[scaleKey] ?? 1 : 1;
    const plain = digits!.replace(/,/g, "");
    const integer = !decimals;
    const raw = Number(`${plain}${decimals ?? ""}`);
    if (!Number.isFinite(raw)) continue;
    const bare = !currency && !percent && !scaleKey;
    if (bare && integer && !digits!.includes(",") && raw >= 1900 && raw <= 2100) continue;
    if (bare && integer && raw < 10) continue;
    if (bare && TIME_UNIT_RE.test(after)) continue;
    if (BOUND_RE.test(before)) continue;
    const places = decimals ? decimals.length - 1 : 0;
    let step = 10 ** -places;
    if (integer && !scaleKey && raw >= 1000) {
      const zeros = /0+$/.exec(plain)?.[0].length ?? 0;
      if (zeros >= 2) step = 10 ** zeros;
    }
    out.push({
      text: whole.trim(),
      value: raw * scale,
      tolerance: (step / 2) * scale + 1e-9 * Math.abs(raw * scale),
      percent,
      index,
    });
  }
  return out;
}

export function parseNumberToken(text: string): AnswerNumber | null {
  return significantNumbers(` ${text.trim()} `)[0] ?? null;
}

function matches(target: AnswerNumber, candidate: number): boolean {
  const value = Math.abs(candidate);
  const goal = Math.abs(target.value);
  if (Math.abs(value - goal) <= target.tolerance) return true;
  return target.percent && Math.abs(value * 100 - goal) <= target.tolerance;
}

// ---------- tool results ----------

type ToolCall = {
  name: string;
  input: unknown;
  done: boolean;
  error?: string;
  result?: unknown;
  data: boolean;
};

function toolCalls(events: readonly AgentEvent[]): ToolCall[] {
  const calls: ToolCall[] = [];
  const byId = new Map<string, ToolCall>();
  for (const event of events) {
    if (event.type === "tool_start") {
      const call: ToolCall = { name: event.name, input: event.input, done: false, data: isDataTool(event.name) };
      calls.push(call);
      byId.set(event.id, call);
    } else if (event.type === "tool_end") {
      const call = byId.get(event.id);
      if (!call) continue;
      call.done = true;
      const error = event.error ?? resultError(event.result);
      if (error) call.error = error;
      else call.result = event.result;
    }
  }
  return calls;
}

function parseMaybeJson(text: string): unknown {
  const trimmed = text.trim();
  if (!/^[[{]/.test(trimmed)) return undefined;
  try {
    return JSON.parse(trimmed);
  } catch {
    return undefined;
  }
}

/** Every JSON value in a tool result, with text content parsed when it holds JSON. */
function expand(value: unknown, depth = 0): unknown[] {
  if (depth > 3) return [value];
  if (typeof value === "string") {
    const parsed = parseMaybeJson(value);
    return parsed === undefined ? [value] : expand(parsed, depth + 1);
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const record = value as Record<string, unknown>;
    if (Array.isArray(record.content)) {
      const texts = record.content.flatMap((part) =>
        part && typeof part === "object" && typeof (part as { text?: unknown }).text === "string"
          ? expand((part as { text: string }).text, depth + 1)
          : [],
      );
      const structured = record.structuredContent === undefined ? [] : expand(record.structuredContent, depth + 1);
      return [...texts, ...structured];
    }
  }
  return [value];
}

function resultError(result: unknown): string | undefined {
  if (!result || typeof result !== "object") return undefined;
  const record = result as Record<string, unknown>;
  if (record.isError === true) return clipText(collectStrings(expand(result)).join(" ") || "tool reported an error");
  for (const value of expand(result)) {
    if (!value || typeof value !== "object" || Array.isArray(value)) continue;
    const body = value as Record<string, unknown>;
    const errors = body.errors;
    if (Array.isArray(errors) && errors.length > 0 && (body.data === null || body.data === undefined)) {
      return clipText(collectStrings([errors]).join(" ") || "query returned errors");
    }
  }
  return undefined;
}

function walk(value: unknown, visit: (value: unknown, key?: string) => void, key?: string, depth = 0): void {
  if (depth > 12) return;
  visit(value, key);
  if (Array.isArray(value)) value.forEach((item) => walk(item, visit, key, depth + 1));
  else if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) walk(v, visit, k, depth + 1);
  }
}

function collectStrings(values: readonly unknown[]): string[] {
  const out: string[] = [];
  for (const value of values) walk(value, (v) => (typeof v === "string" ? out.push(v) : undefined));
  return out;
}

const NUMERIC_STRING_RE = /^-?\d+(?:\.\d+)?$/;
const TEXT_NUMBER_RE = /-?\d{1,3}(?:,\d{3})+(?:\.\d+)?|-?\d+(?:\.\d+)?/g;

/** Numbers a set of tool results holds, with column sums, means and row counts. */
function numberPool(results: readonly unknown[], depth = 0): number[] {
  const pool = new Set<number>();
  const add = (n: number) => {
    if (Number.isFinite(n)) pool.add(Math.abs(n));
  };
  for (const result of results) {
    for (const value of expand(result)) {
      walk(value, (v) => {
        if (typeof v === "number") add(v);
        else if (typeof v === "string") {
          if (NUMERIC_STRING_RE.test(v.trim())) add(Number(v));
          else {
            const parsed = depth < 3 ? parseMaybeJson(v) : undefined;
            if (parsed !== undefined) numberPool([parsed], depth + 1).forEach(add);
            else for (const match of v.matchAll(TEXT_NUMBER_RE)) add(Number(match[0].replace(/,/g, "")));
          }
        } else if (Array.isArray(v)) {
          add(v.length);
          const columns = new Map<string, number[]>();
          for (const row of v) {
            if (!row || typeof row !== "object" || Array.isArray(row)) continue;
            for (const [k, cell] of Object.entries(row as Record<string, unknown>)) {
              const n = typeof cell === "number" ? cell : typeof cell === "string" && NUMERIC_STRING_RE.test(cell.trim()) ? Number(cell) : NaN;
              if (Number.isFinite(n)) columns.set(k, [...(columns.get(k) ?? []), n]);
            }
          }
          for (const cells of columns.values()) {
            const sum = cells.reduce((a, b) => a + b, 0);
            add(sum);
            add(sum / cells.length);
          }
        }
      });
    }
  }
  return [...pool];
}

function grounded(target: AnswerNumber, pool: readonly number[]): boolean {
  return pool.some((value) => matches(target, value));
}

function derived(target: AnswerNumber, pool: readonly number[], listed: readonly number[]): boolean {
  const base = pool.slice(0, MAX_POOL);
  for (const a of base) {
    for (const b of base) {
      if (a === b) continue;
      if (matches(target, a + b) || matches(target, a - b)) return true;
      if (b !== 0 && (matches(target, a / b) || matches(target, (a - b) / b))) return true;
    }
  }
  if (listed.length >= 2 && matches(target, listed.reduce((x, y) => x + y, 0))) return true;
  return false;
}

/** Answer figures that no tool result or the request backs, directly or by a simple derivation. */
function ungroundedFigures(answer: string, pool: readonly number[], request: string): AnswerNumber[] {
  const figures = significantNumbers(answer);
  const requested = significantNumbers(request);
  return figures.filter((figure) => {
    if (requested.some((r) => Math.abs(r.value - figure.value) <= Math.max(r.tolerance, figure.tolerance))) return false;
    if (grounded(figure, pool)) return false;
    const others = figures.filter((other) => other !== figure && !other.percent && !figure.percent);
    const before = others.filter((other) => other.index < figure.index).map((other) => other.value);
    const after = others.filter((other) => other.index > figure.index).map((other) => other.value);
    return !(
      derived(figure, pool, others.map((other) => other.value)) ||
      derived(figure, [], before) ||
      derived(figure, [], after)
    );
  });
}

// ---------- dates ----------

type DateSpan = { text: string; start: number; end: number };

function utc(year: number, month: number, day: number): number {
  return Date.UTC(year, month, day);
}

function monthIndex(name: string): number {
  return MONTHS.indexOf(name.slice(0, 3).toLowerCase());
}

function validYear(year: number): boolean {
  return year >= 1900 && year <= 2100;
}

function daySpan(text: string, year: number, month: number, day: number): DateSpan | null {
  if (!validYear(year) || month < 0 || month > 11 || day < 1 || day > 31) return null;
  const start = utc(year, month, day);
  return { text, start, end: start + DAY_MS - 1 };
}

function monthSpan(text: string, year: number, month: number): DateSpan | null {
  if (!validYear(year) || month < 0 || month > 11) return null;
  return { text, start: utc(year, month, 1), end: utc(year, month + 1, 1) - 1 };
}

/** Day and month dates a text names; ambiguous forms such as 03/04/2024 are left out. */
export function extractDates(text: string): DateSpan[] {
  const spans: DateSpan[] = [];
  let rest = text;
  const take = (re: RegExp, build: (m: RegExpMatchArray) => DateSpan | null) => {
    rest = rest.replace(re, (...args) => {
      const span = build(args.slice(0, -2) as unknown as RegExpMatchArray);
      if (span) spans.push(span);
      return " ".repeat(String(args[0]).length);
    });
  };
  take(/\b(\d{4})-(\d{2})-(\d{2})(?:[T ][\d:.]+(?:Z|[+-]\d{2}:?\d{2})?)?/g, (m) =>
    daySpan(m[0]!, Number(m[1]), Number(m[2]) - 1, Number(m[3])),
  );
  take(/\b(\d{4})-(\d{2})\b/g, (m) => monthSpan(m[0]!, Number(m[1]), Number(m[2]) - 1));
  take(new RegExp(`\\b${MONTH_RE}\\.?\\s+(\\d{1,2})(?:st|nd|rd|th)?,?\\s+(\\d{4})\\b`, "gi"), (m) =>
    daySpan(m[0]!, Number(m[3]), monthIndex(m[1]!), Number(m[2])),
  );
  take(new RegExp(`\\b(\\d{1,2})(?:st|nd|rd|th)?\\s+${MONTH_RE}\\.?,?\\s+(\\d{4})\\b`, "gi"), (m) =>
    daySpan(m[0]!, Number(m[3]), monthIndex(m[2]!), Number(m[1])),
  );
  take(new RegExp(`\\b${MONTH_RE}\\.?\\s+(\\d{4})\\b`, "gi"), (m) => monthSpan(m[0]!, Number(m[2]), monthIndex(m[1]!)));
  return spans;
}

function resultDates(results: readonly unknown[]): DateSpan[] {
  const spans: DateSpan[] = [];
  for (const result of results) {
    for (const value of expand(result)) {
      walk(value, (v) => {
        if (typeof v !== "string") return;
        for (const match of v.matchAll(/\b(\d{4})-(\d{2})(?:-(\d{2}))?/g)) {
          const span = match[3]
            ? daySpan(match[0], Number(match[1]), Number(match[2]) - 1, Number(match[3]))
            : monthSpan(match[0], Number(match[1]), Number(match[2]) - 1);
          if (span) spans.push(span);
        }
      });
    }
  }
  return spans;
}

function overlaps(a: DateSpan, start: number, end: number): boolean {
  return a.start <= end && a.end >= start;
}

function isoDay(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10);
}

// ---------- checks ----------

export type AnswerCheckInput = {
  request: string;
  answer: string;
  events: readonly AgentEvent[];
  /** When the run started; dates near it need no data backing. */
  runAt?: Date;
};

const TOTAL_CLAIM_RE = /\b(total|in total|overall|altogether|combined|sum of|across all|grand total)\b/i;
const RANK_CLAIM_RE = /\b(top|highest|lowest|largest|smallest|most|least|best|worst|biggest|rank(?:s|ed|ing)?|leads?|leading)\b|#1\b/i;
const TRUNCATION_KEY_RE = /^(truncated|is_?truncated|has_?more|has_?next_?page|more_?rows)$/i;
const TRUNCATION_TEXT_RE = /\b(truncated|more rows (?:are )?available|has more (?:rows|results|pages)|next page)\b/i;
const AGGREGATE_KEY_RE = /^(count|sum|avg|min|max)_/i;

function clipText(text: string, max = 160): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
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

const CLIPPED_RE = /…\s*$|\[truncated|chars? omitted|output truncated/i;

/** A result counts only when it was recorded whole: present, parseable and unclipped. */
function recordedInFull(call: ToolCall): boolean {
  if (call.result === undefined || call.result === null) return false;
  const values = expand(call.result);
  if (values.length === 0) return false;
  return values.every((value) =>
    typeof value === "string"
      ? value.trim().length > 0 && !/^\s*[[{]/.test(value) && !CLIPPED_RE.test(value)
      : value !== null && typeof value === "object",
  );
}

/** Successful tool results plus every tool input, so thresholds the agent chose count as backed. */
function groundingSources(calls: readonly ToolCall[], keep: (call: ToolCall, index: number) => boolean): unknown[] {
  return calls.flatMap((call, index) =>
    keep(call, index) ? [call.input, ...(call.done && !call.error ? [call.result] : [])] : [],
  );
}

function checkNumbers(input: AnswerCheckInput, calls: readonly ToolCall[]): AnswerCheck {
  const id = "ungrounded_number" as const;
  const figures = significantNumbers(input.answer);
  if (figures.length === 0) return { id, verdict: "pass", detail: "answer states no figures" };
  const data = calls.filter((call) => call.data && call.done && !call.error);
  if (data.length === 0) return { id, verdict: "unknown", detail: "no successful data-tool result" };
  if (data.some((call) => !recordedInFull(call))) {
    return { id, verdict: "unknown", detail: "a data-tool result was not recorded in full" };
  }
  const pool = numberPool(groundingSources(calls, () => true));
  const missing = ungroundedFigures(input.answer, pool, input.request);
  if (missing.length === 0) return { id, verdict: "pass", detail: `${figures.length} figure(s) found in data results` };
  return {
    id,
    verdict: "fail",
    detail: `${missing.length === 1 ? "number" : "numbers"} ${missing
      .slice(0, 3)
      .map((figure) => figure.text)
      .join(", ")} not found in any data result`,
  };
}

function checkDates(input: AnswerCheckInput, calls: readonly ToolCall[]): AnswerCheck {
  const id = "ungrounded_date" as const;
  const stated = extractDates(input.answer);
  if (stated.length === 0) return { id, verdict: "pass", detail: "answer states no dates" };
  const data = calls.filter((call) => call.data && call.done && !call.error);
  if (data.length === 0) return { id, verdict: "unknown", detail: "no successful data-tool result" };
  const seen = resultDates(data.map((call) => call.result));
  if (seen.length === 0) return { id, verdict: "unknown", detail: "data results hold no dates" };
  const min = Math.min(...seen.map((span) => span.start));
  const max = Math.max(...seen.map((span) => span.end));
  const requested = extractDates(input.request);
  const queried = extractDates(data.map((call) => stringify(call.input)).join("\n"));
  const runAt = input.runAt?.getTime();
  const outside = stated.filter((span) => {
    if (overlaps(span, min - 31 * DAY_MS, max + 31 * DAY_MS)) return false;
    if (requested.some((r) => overlaps(span, r.start, r.end))) return false;
    if (queried.some((q) => overlaps(span, q.start, q.end)) && overlaps(span, min - 366 * DAY_MS, max + 366 * DAY_MS)) {
      return false;
    }
    if (runAt !== undefined && overlaps(span, runAt - 2 * DAY_MS, runAt + 2 * DAY_MS)) return false;
    return true;
  });
  if (outside.length === 0) return { id, verdict: "pass", detail: "stated dates fall inside the data range" };
  return {
    id,
    verdict: "fail",
    detail: `${outside.length === 1 ? "date" : "dates"} ${outside
      .slice(0, 3)
      .map((span) => span.text)
      .join(", ")} outside the data range ${isoDay(min)} to ${isoDay(max)}`,
  };
}

function requestLimit(input: unknown): number | null {
  const text = stringify(input);
  const match = /\b(?:limit|first)\\?"?\s*:\s*(\d+)/i.exec(text);
  return match ? Number(match[1]) : null;
}

function truncation(call: ToolCall): { truncated: boolean; detail: string } {
  const values = expand(call.result);
  let rows = -1;
  let flagged = false;
  let aggregate = false;
  for (const value of values) {
    if (typeof value === "string" && TRUNCATION_TEXT_RE.test(value)) flagged = true;
    walk(value, (v, key) => {
      if (key && TRUNCATION_KEY_RE.test(key) && v === true) flagged = true;
      if (key && AGGREGATE_KEY_RE.test(key)) aggregate = true;
      if (Array.isArray(v) && v.every((row) => row && typeof row === "object" && !Array.isArray(row))) {
        rows = Math.max(rows, v.length);
      }
    });
  }
  if (aggregate) return { truncated: false, detail: "" };
  if (flagged) return { truncated: true, detail: `${call.name} reported more rows than it returned` };
  const limit = requestLimit(call.input);
  if (limit !== null && limit > 0 && rows === limit) {
    return { truncated: true, detail: `${call.name} returned ${rows} rows, equal to its limit ${limit}` };
  }
  return { truncated: false, detail: "" };
}

function checkTruncation(input: AnswerCheckInput, calls: readonly ToolCall[]): AnswerCheck {
  const id = "truncated_data" as const;
  const data = calls.filter((call) => call.data && call.done && !call.error);
  if (data.length === 0) return { id, verdict: "unknown", detail: "no successful data-tool result" };
  const totalClaim = TOTAL_CLAIM_RE.test(input.answer) && significantNumbers(input.answer).length > 0;
  const rankClaim = RANK_CLAIM_RE.test(input.answer);
  if (!totalClaim && !rankClaim) return { id, verdict: "pass", detail: "answer states no total or ranking" };
  for (let i = 0; i < data.length; i += 1) {
    const call = data[i]!;
    const cut = truncation(call);
    if (!cut.truncated) continue;
    const sorted = /\border_by\b/i.test(stringify(call.input));
    if (!totalClaim && sorted) continue;
    if (i < data.length - 1) return { id, verdict: "unknown", detail: `${cut.detail}; later queries followed` };
    return {
      id,
      verdict: "fail",
      detail: `${cut.detail}, and the answer states a ${totalClaim ? "total" : "ranking"} from it`,
    };
  }
  return { id, verdict: "pass", detail: "no truncated data result" };
}

function checkAfterFailure(input: AnswerCheckInput, calls: readonly ToolCall[]): AnswerCheck {
  const id = "answer_after_failure" as const;
  const data = calls.filter((call) => call.data);
  const last = data.at(-1);
  if (!last) return { id, verdict: "unknown", detail: "no data-tool call" };
  if (!last.done) return { id, verdict: "unknown", detail: "the last data-tool call has no recorded end" };
  if (!last.error) return { id, verdict: "pass", detail: "the last data-tool call succeeded" };
  const lastIndex = calls.lastIndexOf(last);
  const pool = numberPool(groundingSources(calls, (_, index) => index !== lastIndex));
  const missing = ungroundedFigures(input.answer, pool, input.request);
  if (missing.length === 0) return { id, verdict: "pass", detail: "every figure comes from an earlier result" };
  return {
    id,
    verdict: "fail",
    detail: `answer states ${missing
      .slice(0, 3)
      .map((figure) => figure.text)
      .join(", ")} after the last data query failed (${clipText(last.error, 120)})`,
  };
}

/** Deterministic, model-free checks on a finished run's answer against its raw tool results. */
export function runAnswerChecks(input: AnswerCheckInput): AnswerCheck[] {
  const calls = toolCalls(input.events);
  return [
    checkNumbers(input, calls),
    checkDates(input, calls),
    checkTruncation(input, calls),
    checkAfterFailure(input, calls),
  ];
}

// ---------- recomputation ----------

/** The data-tool queries a run made, for the verifier's reference. */
export function runDataQueries(events: readonly AgentEvent[], max = 8, maxChars = 800): string[] {
  return toolCalls(events)
    .filter((call) => call.data)
    .slice(-max)
    .map((call) => `${call.name}: ${clipText(stringify(call.input), maxChars)}${call.error ? " -> error" : ""}`);
}

export function hasNumericAnswer(answer: string): boolean {
  return significantNumbers(answer).length > 0;
}

/** The first numeric leaf at a dotted path, or the only numeric leaf when the path misses. */
export function numberAtPath(data: unknown, path: string): number | null {
  const parts = path
    .replace(/^data\./, "")
    .split(/[.[\]]+/)
    .filter(Boolean);
  let cursor: unknown = data;
  for (const part of parts) {
    if (Array.isArray(cursor) && !/^\d+$/.test(part)) cursor = cursor[0];
    if (Array.isArray(cursor)) cursor = cursor[Number(part)];
    else if (cursor && typeof cursor === "object") cursor = (cursor as Record<string, unknown>)[part];
    else {
      cursor = undefined;
      break;
    }
  }
  if (Array.isArray(cursor) && cursor.length === 1) cursor = cursor[0];
  const direct = toNumber(cursor);
  if (direct !== null) return direct;
  const leaves: number[] = [];
  walk(data, (v) => {
    const n = typeof v === "number" ? v : null;
    if (n !== null && Number.isFinite(n)) leaves.push(n);
  });
  return leaves.length === 1 ? leaves[0]! : null;
}

function toNumber(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && NUMERIC_STRING_RE.test(value.trim())) return Number(value);
  return null;
}

export function formatFigure(value: number): string {
  const rounded = Math.abs(value) >= 100 ? Math.round(value) : Math.round(value * 100) / 100;
  return rounded.toLocaleString("en-US");
}

/** Agree when the independent value matches the answer within the relative tolerance or its shown precision. */
export function compareRecompute(input: {
  answerValue: string;
  recomputed: number | null;
  tolerance?: number;
  quantity?: string;
}): { verdict: RecomputeVerdict; detail: string } {
  const label = (input.quantity ?? "value").replace(/\s+/g, " ").trim().slice(0, 60) || "value";
  const target = parseNumberToken(input.answerValue);
  if (!target) return { verdict: "unknown", detail: `answer value ${clipText(input.answerValue, 40)} is not a figure` };
  if (input.recomputed === null || !Number.isFinite(input.recomputed)) {
    return { verdict: "unknown", detail: `independent ${label} returned no number` };
  }
  const tolerance = input.tolerance ?? DEFAULT_RECOMPUTE_TOLERANCE;
  const value = input.recomputed;
  const close = (candidate: number) =>
    Math.abs(Math.abs(candidate) - Math.abs(target.value)) <=
    Math.max(target.tolerance, tolerance * Math.max(Math.abs(candidate), Math.abs(target.value)));
  const agree = close(value) || (target.percent && close(value * 100));
  return {
    verdict: agree ? "agree" : "disagree",
    detail: `independent ${label} ${formatFigure(value)} vs answer ${target.text}`,
  };
}
