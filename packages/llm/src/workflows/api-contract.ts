import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { agentLimits } from "../agent-limits";

export const WORKFLOW_API_TOKEN_PREFIX = "onk_wf";
export const WORKFLOW_API_HARD_MAX_REQUEST_BYTES = 1024 * 1024;
export const WORKFLOW_API_HARD_MAX_BATCH_RECORDS = 10_000;
export const WORKFLOW_API_DEFAULT_RETENTION_HOURS = 168;
export const WORKFLOW_API_INPUT_FILE = "trigger_input.json";

export type WorkflowApiExecutionMode = "single" | "batch";

export type WorkflowApiLimits = {
  requestLimitPerMinute: number;
  pollLimitPerMinute: number;
  queueCap: number;
  concurrencyCap: number;
  batchMaxRecords: number;
  batchChunkSize: number;
  maxRequestBytes: number;
  maxResultBytes: number;
  maxArtifactBytes: number;
  maxRuntimeSeconds: number;
  maxModelCalls: number;
  maxToolCalls: number;
  maxTokensPerRun: number;
  maxCostMicrosPerRun: number;
  rollingWindowSeconds: number;
  rollingTokenBudget: number;
  rollingCostMicrosBudget: number;
  retentionHours: number;
};

export const DEFAULT_WORKFLOW_API_LIMITS: WorkflowApiLimits = {
  requestLimitPerMinute: 30,
  pollLimitPerMinute: 120,
  queueCap: 25,
  concurrencyCap: 2,
  batchMaxRecords: 1_000,
  batchChunkSize: 100,
  maxRequestBytes: 256 * 1024,
  maxResultBytes: 256 * 1024,
  maxArtifactBytes: 10 * 1024 * 1024,
  maxRuntimeSeconds: 900,
  maxModelCalls: 301,
  maxToolCalls: 150,
  maxTokensPerRun: 2_000_000,
  maxCostMicrosPerRun: 5_000_000,
  rollingWindowSeconds: 86_400,
  rollingTokenBudget: 10_000_000,
  rollingCostMicrosBudget: 10_000_000,
  retentionHours: WORKFLOW_API_DEFAULT_RETENTION_HOURS,
};

/**
 * Limits for a workflow whose API access is turned on for the first time. The
 * runtime and tool-call ceilings come from the workflow agent limits; each tool
 * result costs one model call, plus one for a GraphJin agent tool.
 */
export function defaultWorkflowApiLimits(): WorkflowApiLimits {
  const workflow = agentLimits("workflow");
  const clamp = (key: keyof typeof WORKFLOW_API_LIMIT_RANGES, value: number) => {
    const [min, max] = WORKFLOW_API_LIMIT_RANGES[key];
    return Math.min(max, Math.max(min, Math.round(value)));
  };
  const toolCalls = clamp("maxToolCalls", workflow.maxToolCalls ?? workflow.maxTurns);
  return {
    ...DEFAULT_WORKFLOW_API_LIMITS,
    maxRuntimeSeconds: clamp("maxRuntimeSeconds", workflow.timeoutMs / 1_000),
    maxToolCalls: toolCalls,
    maxModelCalls: clamp("maxModelCalls", 2 * toolCalls + 1),
  };
}

export const WORKFLOW_API_LIMIT_RANGES = {
  requestLimitPerMinute: [1, 600],
  pollLimitPerMinute: [1, 1_200],
  queueCap: [1, 250],
  concurrencyCap: [1, 20],
  batchMaxRecords: [1, WORKFLOW_API_HARD_MAX_BATCH_RECORDS],
  batchChunkSize: [1, 500],
  maxRequestBytes: [1_024, WORKFLOW_API_HARD_MAX_REQUEST_BYTES],
  maxResultBytes: [1_024, 1024 * 1024],
  maxArtifactBytes: [1_024, 50 * 1024 * 1024],
  maxRuntimeSeconds: [30, 1_800],
  maxModelCalls: [1, 1_024],
  maxToolCalls: [1, 1_024],
  maxTokensPerRun: [1_000, 3_000_000],
  maxCostMicrosPerRun: [1_000, 100_000_000],
  rollingWindowSeconds: [3_600, 604_800],
  rollingTokenBudget: [1_000, 10_000_000],
  rollingCostMicrosBudget: [1_000, 1_000_000_000],
  retentionHours: [1, 720],
} as const satisfies Record<keyof WorkflowApiLimits, readonly [number, number]>;

export class WorkflowApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly retryAfterSeconds?: number;

  constructor(
    code: string,
    message: string,
    status: number,
    retryAfterSeconds?: number,
  ) {
    super(message);
    this.name = "WorkflowApiError";
    this.code = code;
    this.status = status;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export type IssuedWorkflowApiToken = {
  token: string;
  verifier: string;
  prefix: string;
};

/** Generate 256 bits of entropy; only the digest and display prefix persist. */
export function issueWorkflowApiToken(): IssuedWorkflowApiToken {
  const secret = randomBytes(32).toString("base64url");
  const fingerprint = randomBytes(6).toString("hex");
  const token = `${WORKFLOW_API_TOKEN_PREFIX}_${fingerprint}_${secret}`;
  return {
    token,
    verifier: workflowApiTokenVerifier(token),
    prefix: `${WORKFLOW_API_TOKEN_PREFIX}_${fingerprint}`,
  };
}

export function workflowApiTokenVerifier(token: string): string {
  return createHash("sha256").update(token, "utf8").digest("hex");
}

/** Always performs one same-length timing-safe comparison, even when absent. */
export function verifyWorkflowApiTokenDigest(
  token: string,
  expectedVerifier: string | null | undefined,
): boolean {
  const actual = Buffer.from(workflowApiTokenVerifier(token), "hex");
  const expectedHex =
    typeof expectedVerifier === "string" && /^[0-9a-f]{64}$/i.test(expectedVerifier)
      ? expectedVerifier
      : "0".repeat(64);
  const expected = Buffer.from(expectedHex, "hex");
  const matches = timingSafeEqual(actual, expected);
  return matches && expectedVerifier === expectedHex;
}

export type CompiledWorkflowBatchColumn = {
  name: string;
  path: string;
  default?: string | number | boolean | null;
};

/**
 * Operator-authored, workflow-native batch shape. This belongs to the
 * workflow definition itself: a workflow may use no skills, one skill, or
 * several skills without changing this contract.
 */
export type WorkflowApiBatchDefinition = {
  recordsField?: string;
  columns: CompiledWorkflowBatchColumn[];
};

export type CompiledWorkflowBatchContract = {
  version: 1;
  compiled: true;
  compiler: "workflow";
  recordsField: string;
  columns: CompiledWorkflowBatchColumn[];
};

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

const UNSAFE_PATH_PARTS = new Set(["__proto__", "prototype", "constructor"]);

/**
 * Batch execution is fail-closed. The workflow save boundary compiles an
 * explicit workflow-native batch definition into this small deterministic
 * projection contract. Skills are deliberately not part of readiness: a
 * workflow may use zero, one, or many of them. Arbitrary prompt prose is never
 * interpreted as a batch program.
 */
export function parseCompiledWorkflowBatchContract(
  outputContract: Record<string, unknown> | null | undefined,
): CompiledWorkflowBatchContract | null {
  const raw = outputContract?.apiBatch;
  if (!isPlainObject(raw)) return null;
  if (
    raw.version !== 1 ||
    raw.compiled !== true ||
    raw.compiler !== "workflow"
  ) {
    return null;
  }
  const recordsField =
    typeof raw.recordsField === "string" &&
    /^[a-zA-Z][a-zA-Z0-9_]{0,63}$/.test(raw.recordsField)
      ? raw.recordsField
      : "records";
  if (
    !Array.isArray(raw.columns) ||
    raw.columns.length === 0 ||
    raw.columns.length > 64
  ) {
    return null;
  }
  const seen = new Set<string>();
  const columns: CompiledWorkflowBatchColumn[] = [];
  for (const candidate of raw.columns) {
    if (!isPlainObject(candidate)) return null;
    const name = candidate.name;
    const path = candidate.path;
    if (
      typeof name !== "string" ||
      name.length === 0 ||
      name.length > 80 ||
      /[\r\n]/.test(name) ||
      seen.has(name) ||
      typeof path !== "string" ||
      path.length === 0 ||
      path.length > 256
    ) {
      return null;
    }
    const parts = path.split(".");
    if (
      parts.some(
        (part) =>
          !/^[a-zA-Z0-9_-]{1,80}$/.test(part) || UNSAFE_PATH_PARTS.has(part),
      )
    ) {
      return null;
    }
    const fallback = candidate.default;
    if (
      fallback !== undefined &&
      fallback !== null &&
      typeof fallback !== "string" &&
      typeof fallback !== "number" &&
      typeof fallback !== "boolean"
    ) {
      return null;
    }
    seen.add(name);
    columns.push({
      name,
      path,
      ...(fallback !== undefined ? { default: fallback } : {}),
    });
  }
  return {
    version: 1,
    compiled: true,
    compiler: "workflow",
    recordsField,
    columns,
  };
}

/**
 * Compile an explicit workflow batch definition at the trusted save boundary.
 * Callers cannot mark arbitrary payloads as compiled, and invalid/unsafe
 * projections never reach workflow storage.
 */
export function compileWorkflowApiBatchContract(
  definition: WorkflowApiBatchDefinition,
): CompiledWorkflowBatchContract {
  const parsed = parseCompiledWorkflowBatchContract({
    apiBatch: {
      version: 1,
      compiled: true,
      compiler: "workflow",
      recordsField: definition.recordsField ?? "records",
      columns: definition.columns,
    },
  });
  if (!parsed) {
    throw new WorkflowApiError(
      "invalid_batch_contract",
      "The workflow batch definition is invalid or unsafe.",
      400,
    );
  }
  return parsed;
}

export function workflowApiLimitPatch(
  value: unknown,
): Partial<WorkflowApiLimits> {
  if (!isPlainObject(value)) {
    throw new WorkflowApiError(
      "invalid_limits",
      "Limits must be a JSON object.",
      400,
    );
  }
  const result: Partial<WorkflowApiLimits> = {};
  for (const [key, candidate] of Object.entries(value)) {
    if (!(key in WORKFLOW_API_LIMIT_RANGES)) {
      throw new WorkflowApiError(
        "unknown_limit",
        `Unknown workflow API limit: ${key}.`,
        400,
      );
    }
    if (!Number.isSafeInteger(candidate)) {
      throw new WorkflowApiError(
        "invalid_limit",
        `${key} must be an integer.`,
        400,
      );
    }
    const typedKey = key as keyof WorkflowApiLimits;
    const [minimum, maximum] = WORKFLOW_API_LIMIT_RANGES[typedKey];
    if ((candidate as number) < minimum || (candidate as number) > maximum) {
      throw new WorkflowApiError(
        "limit_out_of_range",
        `${key} must be between ${minimum} and ${maximum}.`,
        400,
      );
    }
    result[typedKey] = candidate as never;
  }
  if (
    result.batchChunkSize !== undefined &&
    result.batchMaxRecords !== undefined &&
    result.batchChunkSize > result.batchMaxRecords
  ) {
    throw new WorkflowApiError(
      "invalid_batch_limits",
      "batchChunkSize cannot exceed batchMaxRecords.",
      400,
    );
  }
  return result;
}

type AnswerTable = {
  columns: Array<{ key: string; label: string }>;
  rows: Array<Record<string, string | number>>;
};

function cellValue(value: unknown): string | number {
  const text = String(value ?? "").trim();
  return /^-?\d{1,3}(,\d{3})*(\.\d+)?$|^-?\d+(\.\d+)?$/.test(text) ? Number(text.replaceAll(",", "")) : text;
}

function answerKeys(labels: string[]): string[] {
  const seen = new Map<string, number>();
  return labels.map((label, index) => {
    const base = label.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || `column_${index + 1}`;
    const count = (seen.get(base) ?? 0) + 1;
    seen.set(base, count);
    return count > 1 ? `${base}_${count}` : base;
  });
}

function answerTable(component: Record<string, unknown>): AnswerTable | null {
  const columns = Array.isArray(component.columns) ? component.columns : [];
  const rows = Array.isArray(component.rows) ? component.rows : [];
  const source = columns
    .filter(isPlainObject)
    .map((column) => ({ key: String(column.key ?? ""), label: String(column.label ?? column.key ?? "") }));
  if (!source.length) return null;
  const keys = answerKeys(source.map((column) => column.label));
  return {
    columns: source.map((column, index) => ({ key: keys[index], label: column.label })),
    rows: rows.filter(isPlainObject).map((row) =>
      Object.fromEntries(source.map((column, index) => [keys[index], cellValue(row[column.key])]))),
  };
}

/**
 * The answer card of an API run as data: its title, main table, the sub-table
 * behind each row, and its callout. Null when the run drew no card with a table.
 */
export function workflowApiAnswer(messages: readonly unknown[]): Record<string, unknown> | null {
  const components = messages
    .filter(isPlainObject)
    .flatMap((message) => {
      const surface = (message.createSurface ?? message.updateComponents) as unknown;
      return isPlainObject(surface) && Array.isArray(surface.components) ? surface.components : [];
    })
    .filter(isPlainObject);
  const table = components.find((component) => component.component === "Table");
  const main = table ? answerTable(table) : null;
  if (!table || !main) return null;
  const title = components.find((component) => component.component === "Answer")?.title;
  const callout = components.find((component) => component.component === "Callout")?.text;
  const details = isPlainObject(table.details)
    ? Object.fromEntries(
        Object.entries(table.details)
          .map(([name, sub]) => [name, isPlainObject(sub) ? answerTable(sub) : null] as const)
          .filter((entry): entry is readonly [string, AnswerTable] => entry[1] !== null),
      )
    : {};
  return {
    ...(typeof title === "string" ? { title } : {}),
    table: main,
    ...(Object.keys(details).length ? { details } : {}),
    ...(typeof callout === "string" ? { callout } : {}),
  };
}

export function boundedWorkflowApiResult(
  finalText: string,
  maxBytes: number,
  answer?: Record<string, unknown> | null,
): Record<string, unknown> {
  const trimmed = finalText.trim();
  if (trimmed) {
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      if (isPlainObject(parsed)) {
        const encoded = JSON.stringify(parsed);
        if (Buffer.byteLength(encoded, "utf8") <= maxBytes) return parsed;
      }
    } catch {
      // Plain-text workflow summaries remain a valid machine-readable object.
    }
  }
  if (answer) {
    const withAnswer = { text: trimmed, answer, truncated: false };
    if (Buffer.byteLength(JSON.stringify(withAnswer), "utf8") <= maxBytes) return withAnswer;
  }
  const complete = { text: trimmed, truncated: false };
  if (Buffer.byteLength(JSON.stringify(complete), "utf8") <= maxBytes) {
    return complete;
  }

  // Binary-search a JSON-encoded prefix rather than slicing a byte buffer: it
  // keeps Unicode valid and includes the envelope + truncation marker in the
  // exact result-size ceiling.
  let low = 0;
  let high = trimmed.length;
  let best = "";
  while (low <= high) {
    const midpoint = Math.floor((low + high) / 2);
    const candidateText = `${trimmed.slice(0, midpoint)}…`;
    const candidate = { text: candidateText, truncated: true };
    if (Buffer.byteLength(JSON.stringify(candidate), "utf8") <= maxBytes) {
      best = candidateText;
      low = midpoint + 1;
    } else {
      high = midpoint - 1;
    }
  }
  const truncated = { text: best, truncated: true };
  if (Buffer.byteLength(JSON.stringify(truncated), "utf8") <= maxBytes) {
    return truncated;
  }
  return Buffer.byteLength('{"truncated":true}', "utf8") <= maxBytes
    ? { truncated: true }
    : {};
}
