import { describe, expect, it } from "vitest";
import {
  decideGate,
  expandedLevel,
  pocockLevel,
  sequentialState,
  validateLooks,
  type SequentialGateSpec,
  type SequentialObservation,
} from "../src/sequential";

const H1: SequentialGateSpec = { id: "h1", metric: "answer", treatment: "T", control: "C", threshold: 0, strict: true };

function mulberry(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Runs the sequential loop to its stop with outcomes drawn per task, arm and repetition. */
function simulate(input: {
  seed: number;
  tasks: number;
  looks: number[];
  rate: (task: number, arm: "T" | "C", random: () => number) => number;
  iterations?: number;
}) {
  const random = mulberry(input.seed);
  const tasks = Array.from({ length: input.tasks }, (_, index) => `test/t${index}`);
  const rates = tasks.map((_, index) => ({ T: input.rate(index, "T", random), C: input.rate(index, "C", random) }));
  const observations: SequentialObservation[] = [];
  const have = new Set<string>();
  for (;;) {
    const state = sequentialState({
      looks: input.looks,
      tasks,
      arms: ["T", "C"],
      metrics: ["answer"],
      gates: [H1],
      observations,
      ...(input.iterations ? { iterations: input.iterations } : {}),
    });
    if (state.stopped) return { state, episodes: observations.length };
    tasks.forEach((task, index) => {
      for (const arm of ["T", "C"] as const) {
        for (let repetition = 1; repetition <= state.targets.get(task)!; repetition += 1) {
          const key = `${task}/${arm}/${repetition}`;
          if (have.has(key)) continue;
          have.add(key);
          observations.push({ taskId: task, arm, repetition, outcomes: { answer: random() < rates[index]![arm] } });
        }
      }
    });
  }
}

describe("sequential looks", () => {
  it("uses the Pocock level for the number of looks", () => {
    expect(pocockLevel(1)).toBeCloseTo(0.95);
    expect(pocockLevel(3)).toBeCloseTo(0.9779);
    expect(pocockLevel(12)).toBeCloseTo(1 - 0.05 / 12);
    expect(expandedLevel(0.95, 18)).toBeGreaterThan(0.95);
    expect(expandedLevel(0.95, 1000)).toBeCloseTo(0.95, 2);
  });

  it("requires increasing looks that end at the configured repetitions", () => {
    expect(validateLooks([2, 3, 5], 5)).toEqual([2, 3, 5]);
    expect(() => validateLooks([2, 2, 5], 5)).toThrow(/increasing/);
    expect(() => validateLooks([2, 3], 5)).toThrow(/end at 5/);
  });
});

describe("decideGate", () => {
  const rows = (diffs: Array<[number, number]>) =>
    diffs.flatMap(([treated, control], task) =>
      [0, 1].flatMap((repetition) => [
        { taskId: `test/t${task}`, arm: "T", repetition: repetition + 1, passed: repetition < treated },
        { taskId: `test/t${task}`, arm: "C", repetition: repetition + 1, passed: repetition < control },
      ]),
    );

  it("passes a clear effect at an early look", () => {
    const result = decideGate({
      outcomes: rows(Array.from({ length: 16 }, () => [2, 0] as [number, number])),
      gate: H1,
      level: pocockLevel(3),
      final: false,
      remaining: () => 3,
    });
    expect(result.decision).toBe("pass");
  });

  it("stops for futility when even the best case cannot clear the bound", () => {
    const result = decideGate({
      outcomes: rows(Array.from({ length: 16 }, () => [0, 2] as [number, number])),
      gate: H1,
      level: pocockLevel(3),
      final: false,
      remaining: () => 1,
    });
    expect(result.decision).toBe("fail");
    expect(result.bestCaseLow).not.toBeNull();
  });

  it("continues while the remaining repetitions could still clear the bound", () => {
    const diffs = Array.from({ length: 16 }, (_, task) => (task % 3 ? [2, 1] : [1, 2]) as [number, number]);
    const result = decideGate({
      outcomes: rows(diffs),
      gate: H1,
      level: pocockLevel(3),
      final: false,
      remaining: () => 3,
    });
    expect(result.decision).toBe("continue");
    expect(decideGate({ outcomes: rows(diffs), gate: H1, level: pocockLevel(3), final: true, remaining: () => 0 }).decision).toBe("fail");
  });
});

describe("sequentialState", () => {
  it("asks for the first look, then only for tasks that disagree", () => {
    const tasks = ["test/a1", "test/a2", ...Array.from({ length: 10 }, (_, index) => `test/b${index + 1}`)];
    const look = (observations: SequentialObservation[]) =>
      sequentialState({ looks: [2, 5], tasks, arms: ["T", "C"], metrics: ["answer"], gates: [H1], observations });
    const first = look([]);
    expect(first).toMatchObject({ look: 0, complete: false, stopped: false });
    expect(first.targets.get("test/a1")).toBe(2);
    const observations: SequentialObservation[] = tasks.flatMap((taskId) =>
      ["T", "C"].flatMap((arm) =>
        [1, 2].map((repetition) => ({
          taskId,
          arm,
          repetition,
          outcomes: { answer: taskId.startsWith("test/a") || repetition === 1 },
        })),
      ),
    );
    const second = look(observations);
    expect(second).toMatchObject({ look: 1, complete: false, stopped: false });
    expect(second.targets.get("test/a1")).toBe(2);
    expect(second.targets.get("test/b1")).toBe(5);
    expect([...second.settled]).toEqual(["test/a1", "test/a2"]);
  });

  it("stops early on a clear effect and runs every look on an unclear one", () => {
    const clear = simulate({ seed: 7, tasks: 18, looks: [2, 3, 5], rate: (_, arm) => (arm === "T" ? 0.95 : 0.1) });
    expect(clear.state.look).toBe(0);
    expect(clear.state.gates[0]!.decision).toBe("pass");
    const unclear = simulate({ seed: 11, tasks: 18, looks: [2, 3, 5], rate: (_, arm) => (arm === "T" ? 0.55 : 0.5) });
    expect(unclear.state.look).toBeGreaterThan(0);
  });

  it("keeps the Type I error near the nominal one-sided 2.5% under no effect", () => {
    const runs = 800;
    let passes = 0;
    let episodes = 0;
    for (let seed = 1; seed <= runs; seed += 1) {
      let base = 0;
      const result = simulate({
        seed,
        tasks: 18,
        looks: [2, 3, 5],
        iterations: 400,
        rate: (_, arm, random) => (arm === "T" ? (base = 0.1 + 0.8 * random()) : base),
      });
      if (result.state.gates[0]!.decision === "pass") passes += 1;
      episodes += result.episodes;
    }
    const rate = passes / runs;
    expect(rate).toBeLessThanOrEqual(0.025 + 3 * Math.sqrt((0.025 * 0.975) / runs));
    expect(episodes / runs).toBeLessThan(18 * 2 * 5);
  }, 60_000);
});
