import { pairedArmComparison, type ArmTaskOutcome, type PairedArmComparison } from "./learning";

/** Pocock nominal two-sided alpha per look for an overall two-sided 0.05 with K equally spaced looks. */
const POCOCK_ALPHA: Readonly<Record<number, number>> = {
  1: 0.05,
  2: 0.0294,
  3: 0.0221,
  4: 0.0182,
  5: 0.0158,
  6: 0.0142,
  7: 0.013,
  8: 0.012,
  9: 0.0112,
  10: 0.0106,
};

/** The two-sided CI level used at every one of K looks; past ten looks, Bonferroni. */
export function pocockLevel(looks: number): number {
  if (!Number.isSafeInteger(looks) || looks < 1) throw new Error("looks must be a positive integer");
  return 1 - (POCOCK_ALPHA[looks] ?? 0.05 / looks);
}

function normalQuantile(p: number): number {
  const a = [-39.69683028665376, 220.9460984245205, -275.9285104469687, 138.357751867269, -30.66479806614716, 2.506628277459239];
  const b = [-54.47609879822406, 161.5858368580409, -155.6989798598866, 66.80131188771972, -13.28068155288572];
  const c = [-0.007784894002430293, -0.3223964580411365, -2.400758277161838, -2.549732539343734, 4.374664141464968, 2.938163982698783];
  const d = [0.007784695709041462, 0.3224671290700398, 2.445134137142996, 3.754408661907416];
  const tail = (q: number) =>
    (((((c[0]! * q + c[1]!) * q + c[2]!) * q + c[3]!) * q + c[4]!) * q + c[5]!) /
    ((((d[0]! * q + d[1]!) * q + d[2]!) * q + d[3]!) * q + 1);
  if (p < 0.02425) return tail(Math.sqrt(-2 * Math.log(p)));
  if (p > 1 - 0.02425) return -tail(Math.sqrt(-2 * Math.log(1 - p)));
  const q = p - 0.5;
  const r = q * q;
  return (
    ((((((a[0]! * r + a[1]!) * r + a[2]!) * r + a[3]!) * r + a[4]!) * r + a[5]!) * q) /
    (((((b[0]! * r + b[1]!) * r + b[2]!) * r + b[3]!) * r + b[4]!) * r + 1)
  );
}

function normalCdf(x: number): number {
  const t = 1 / (1 + 0.2316419 * Math.abs(x));
  const poly = t * (0.31938153 + t * (-0.356563782 + t * (1.781477937 + t * (-1.821255978 + t * 1.330274429))));
  const upper = (Math.exp((-x * x) / 2) / Math.sqrt(2 * Math.PI)) * poly;
  return x >= 0 ? 1 - upper : upper;
}

/** Cornish-Fisher quantile of Student's t with df degrees of freedom. */
function studentQuantile(p: number, df: number): number {
  const z = normalQuantile(p);
  const z3 = z ** 3;
  const z5 = z ** 5;
  return (
    z +
    (z3 + z) / (4 * df) +
    (5 * z5 + 16 * z3 + 3 * z) / (96 * df ** 2) +
    (3 * z ** 7 + 19 * z5 + 17 * z3 - 15 * z) / (384 * df ** 3)
  );
}

/**
 * Expanded percentile bootstrap (Hesterberg 2015): the percentile level that
 * matches a t interval with an n - 1 variance divisor. The plain percentile
 * interval is too narrow for a few dozen tasks.
 */
export function expandedLevel(level: number, n: number): number {
  if (n < 3) return level;
  const t = studentQuantile(1 - (1 - level) / 2, n - 1);
  return 1 - 2 * normalCdf(-Math.sqrt(n / (n - 1)) * t);
}

export function validateLooks(looks: readonly number[], repetitions: number): number[] {
  if (
    looks.length === 0 ||
    looks.some((look, index) => !Number.isSafeInteger(look) || look < 1 || (index > 0 && look <= looks[index - 1]!)) ||
    looks[looks.length - 1] !== repetitions
  ) {
    throw new Error(`looks must be increasing repetition counts that end at ${repetitions}`);
  }
  return [...looks];
}

export type GateDecision = "pass" | "fail" | "continue" | "none";

export type SequentialGateSpec = {
  id: string;
  metric: string;
  treatment: string;
  control: string;
  threshold: number;
  /** Strict requires the lower bound above the threshold; otherwise at or above it. */
  strict: boolean;
  /** Only tasks whose ID starts with this prefix count. */
  taskPrefix?: string;
};

/**
 * One look at one gate. Pass: the CI lower bound clears the threshold. Fail at
 * the last look, or futility before it: the lower bound stays short even when
 * every remaining repetition favors the treatment. With a fixed bootstrap seed
 * each resampled mean rises with every task difference, so the best case
 * bounds every reachable lower bound.
 */
export function decideGate(input: {
  outcomes: readonly ArmTaskOutcome[];
  gate: SequentialGateSpec;
  level: number;
  final: boolean;
  remaining: (taskId: string) => number;
  iterations?: number;
}): { decision: GateDecision; comparison: PairedArmComparison; bestCaseLow: number | null } {
  const { gate } = input;
  const rows = input.outcomes.filter((row) => !gate.taskPrefix || row.taskId.startsWith(gate.taskPrefix));
  const seed = `${gate.id}/${gate.treatment}/${gate.control}`;
  const arms = new Map<string, Set<string>>();
  for (const row of rows) (arms.get(row.taskId) ?? arms.set(row.taskId, new Set()).get(row.taskId)!).add(row.arm);
  const paired = [...arms.values()].filter((set) => set.has(gate.treatment) && set.has(gate.control)).length;
  const options = {
    level: expandedLevel(input.level, paired),
    ...(input.iterations ? { iterations: input.iterations } : {}),
  };
  const comparison = pairedArmComparison(rows, gate.treatment, gate.control, seed, options);
  const clears = (low: number) => (gate.strict ? low > gate.threshold : low >= gate.threshold - 1e-9);
  if (!comparison.ci95) return { decision: input.final ? "none" : "continue", comparison, bestCaseLow: null };
  if (clears(comparison.ci95.low)) return { decision: "pass", comparison, bestCaseLow: null };
  if (input.final) return { decision: "fail", comparison, bestCaseLow: null };
  const best: ArmTaskOutcome[] = [...rows];
  for (const task of comparison.perTask) {
    const extra = Math.max(0, input.remaining(task.taskId));
    for (let index = 0; index < extra; index += 1) {
      const repetition = 1_000_000 + index;
      best.push({ taskId: task.taskId, arm: gate.treatment, repetition, passed: true });
      best.push({ taskId: task.taskId, arm: gate.control, repetition, passed: false });
    }
  }
  const bestCaseLow = pairedArmComparison(best, gate.treatment, gate.control, seed, options).ci95?.low ?? null;
  return {
    decision: bestCaseLow !== null && !clears(bestCaseLow) ? "fail" : "continue",
    comparison,
    bestCaseLow,
  };
}

export type SequentialObservation = {
  taskId: string;
  arm: string;
  repetition: number;
  /** Outcome per metric; null when the episode is missing data, such as a provider fault. */
  outcomes: Readonly<Record<string, boolean>> | null;
};

export type SequentialGateResult = {
  id: string;
  decision: GateDecision;
  look: number;
  level: number;
  comparison: PairedArmComparison;
};

export type SequentialState = {
  /** Zero-based index of the look in progress, or of the look that ended the run. */
  look: number;
  /** Repetitions each task needs for that look. */
  targets: Map<string, number>;
  /** Every target repetition has a terminal episode in every arm. */
  complete: boolean;
  /** Every gate is decided; no more repetitions are needed. */
  stopped: boolean;
  gates: SequentialGateResult[];
  /** Tasks with one outcome per metric across every repetition and arm; they get no more repetitions. */
  settled: Set<string>;
};

/**
 * Group-sequential state from the episodes so far. Each look k needs looks[k]
 * repetitions of every unsettled task; settled tasks keep the repetitions they
 * have. A decided gate stays decided. The run stops when every gate is decided.
 */
export function sequentialState(input: {
  looks: readonly number[];
  tasks: readonly string[];
  arms: readonly string[];
  metrics: readonly string[];
  gates: readonly SequentialGateSpec[];
  observations: readonly SequentialObservation[];
  iterations?: number;
}): SequentialState {
  const looks = input.looks;
  const level = pocockLevel(looks.length);
  const seen = new Set(input.observations.map((row) => `${row.taskId}\u0000${row.arm}\u0000${row.repetition}`));
  const decided = new Map<string, SequentialGateResult>();
  let targets = new Map(input.tasks.map((task) => [task, looks[0]!]));
  let settled = new Set<string>();
  for (let look = 0; look < looks.length; look += 1) {
    const missing = input.tasks.some((task) =>
      input.arms.some((arm) => {
        for (let repetition = 1; repetition <= targets.get(task)!; repetition += 1) {
          if (!seen.has(`${task}\u0000${arm}\u0000${repetition}`)) return true;
        }
        return false;
      }),
    );
    if (missing) {
      return { look, targets, complete: false, stopped: false, gates: [...decided.values()], settled };
    }
    const window = input.observations.filter(
      (row) => row.repetition <= (targets.get(row.taskId) ?? 0) && input.arms.includes(row.arm),
    );
    settled = new Set(
      input.tasks.filter((task) =>
        input.metrics.every((metric) => {
          const values = input.arms.map((arm) =>
            window.filter((row) => row.taskId === task && row.arm === arm && row.outcomes).map((row) => row.outcomes![metric]),
          );
          const first = values[0]?.[0];
          return values.every((list) => list.length >= 2 && list.every((value) => value === first));
        }),
      ),
    );
    const final = look === looks.length - 1;
    const last = looks[looks.length - 1]!;
    const remaining = (task: string) => (settled.has(task) ? 0 : last - (targets.get(task) ?? last));
    const gates = input.gates.map((gate): SequentialGateResult => {
      const held = decided.get(gate.id);
      if (held) return held;
      const outcomes: ArmTaskOutcome[] = window
        .filter((row) => row.outcomes && row.outcomes[gate.metric] !== undefined)
        .map((row) => ({ taskId: row.taskId, arm: row.arm, repetition: row.repetition, passed: row.outcomes![gate.metric]! }));
      const { decision, comparison } = decideGate({
        outcomes,
        gate,
        level,
        final,
        remaining,
        ...(input.iterations ? { iterations: input.iterations } : {}),
      });
      const result = { id: gate.id, decision, look, level, comparison };
      if (decision !== "continue") decided.set(gate.id, result);
      return result;
    });
    if (final || input.gates.every((gate) => decided.has(gate.id))) {
      return { look, targets, complete: true, stopped: true, gates, settled };
    }
    const next = looks[look + 1]!;
    targets = new Map(input.tasks.map((task) => [task, settled.has(task) ? targets.get(task)! : next]));
  }
  throw new Error("unreachable");
}

export function sequentialRuleText(looks: readonly number[]): string {
  const level = pocockLevel(looks.length);
  return (
    `Group-sequential design with ${looks.length} look${looks.length === 1 ? "" : "s"} at ${looks.join(", ")} repetitions per task. ` +
    `Each look uses a ${(100 * level).toFixed(2)}% expanded percentile bootstrap CI over tasks (Pocock constant boundary, overall two-sided alpha 0.05; the expansion matches a t interval for a few dozen tasks). ` +
    "A gate passes when its CI lower bound clears the threshold, and fails at the last look or when even the best case for the remaining repetitions cannot clear it. " +
    "A decided gate stays decided, and the run stops when every gate is decided. " +
    "After each look, a task gets more repetitions only while its outcomes disagree across repetitions or arms; a task with one outcome everywhere keeps the repetitions it has."
  );
}
