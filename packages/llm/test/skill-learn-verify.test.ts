import { describe, expect, it } from "vitest";
import type { AgentEvent } from "../src/agent-backend";
import {
  compareRecompute,
  isVerifiedWrong,
  numberAtPath,
  runAnswerChecks,
  sampledForVerify,
  significantNumbers,
  verificationLines,
  verifyRate,
} from "../src/work/skill-learn-verify";

let seq = 0;
function call(name: string, input: unknown, outcome: { result?: unknown; error?: string }): AgentEvent[] {
  const id = `t${(seq += 1)}`;
  return [
    { type: "tool_start", id, name, input },
    { type: "tool_end", id, ...outcome },
  ];
}

function mcp(data: unknown): unknown {
  return { isError: false, content: [{ type: "text", text: JSON.stringify(data) }] };
}

const ORDERS = mcp({
  orders: [
    { id: 1, created_at: "2022-01-04T10:00:00Z", grand_total: 1250.5 },
    { id: 2, created_at: "2023-06-11T10:00:00Z", grand_total: 3161371 },
    { id: 3, created_at: "2025-12-30T10:00:00Z", grand_total: 840 },
  ],
});

function check(id: string, answer: string, events: AgentEvent[], request = "How did revenue do?") {
  return runAnswerChecks({ request, answer, events }).find((c) => c.id === id)!;
}

describe("ungrounded number check", () => {
  const events = call("mcp_graphjin_execute_graphql", { query: "{ orders { id created_at grand_total } }" }, { result: ORDERS });

  it("passes figures in other formats, rounded to the shown precision", () => {
    for (const answer of [
      "Revenue was 3,161,371.",
      "Revenue was $3161371.",
      "Revenue was about 3.16M.",
      "The top order was $3.2 million.",
      "One order totalled 1,250.50 and another 1,251.",
      "Revenue was 3,200,000 overall.",
    ]) {
      expect(check("ungrounded_number", answer, events).verdict, answer).toBe("pass");
    }
  });

  it("passes simple derivations: difference, ratio, percent change, share and sums", () => {
    for (const answer of [
      "The gap between the two orders is 410.5.",
      "The large order is 3,763.54 times the small one.",
      "Order 1 grew 48.87% over order 3.",
      "Order 3 is 67.17% of order 1.",
      "Orders 1 and 3 add up to 2,090.5.",
      "Listed: 1,250.50, 840 and 3,161,371. Total: 3,163,461.50.",
    ]) {
      expect(check("ungrounded_number", answer, events).verdict, answer).toBe("pass");
    }
  });

  it("ignores ranks, years, durations, bounds and request numbers", () => {
    const answer = "Over the last 30 days of 2024, the 3 largest orders (more than 5,000 each) matter; top 25.";
    expect(check("ungrounded_number", answer, events, "Show the top 25 orders").verdict).toBe("pass");
  });

  it("fails a figure that no result backs", () => {
    const result = check("ungrounded_number", "Revenue reached 7,412,906 this year.", events);
    expect(result.verdict).toBe("fail");
    expect(result.detail).toBe("number 7,412,906 not found in any data result");
  });

  it("is unknown without data results or with clipped results", () => {
    expect(check("ungrounded_number", "Revenue was 7,412,906.", []).verdict).toBe("unknown");
    const clipped = call("mcp_graphjin_execute_graphql", { query: "{ orders { id } }" }, { result: '{"orders": [{"id": 1}, …' });
    expect(check("ungrounded_number", "Revenue was 7,412,906.", clipped).verdict).toBe("unknown");
  });

  it("parses figures with their precision", () => {
    expect(significantNumbers("€1.2k and 12.5% on 2024-05-01, rank 3, in 2023").map((n) => [n.text, n.value, n.percent])).toEqual([
      ["€1.2k", 1200, false],
      ["12.5%", 12.5, true],
    ]);
  });
});

describe("ungrounded date check", () => {
  const events = call("mcp_graphjin_execute_graphql", { query: "{ orders { created_at } }" }, { result: ORDERS });

  it("fails the 2014 window when the data covers 2022 to 2025", () => {
    const result = check("ungrounded_date", "Between 2014-03-03 to 2014-05-31 revenue was flat.", events);
    expect(result.verdict).toBe("fail");
    expect(result.detail).toBe("dates 2014-03-03, 2014-05-31 outside the data range 2022-01-04 to 2025-12-30");
  });

  it("passes dates inside the data range, in the request, or near its edges", () => {
    expect(check("ungrounded_date", "From January 2022 to 30 December 2025 revenue grew.", events).verdict).toBe("pass");
    expect(check("ungrounded_date", "Since 2022-01-01, revenue grew.", events).verdict).toBe("pass");
    expect(check("ungrounded_date", "In March 2014 nothing sold.", events, "What sold in March 2014?").verdict).toBe("pass");
  });

  it("is unknown when the results hold no dates", () => {
    const plain = call("mcp_graphjin_execute_graphql", { query: "{ orders { id } }" }, { result: mcp({ orders: [{ id: 1 }] }) });
    expect(check("ungrounded_date", "On 2014-03-03 it sold.", plain).verdict).toBe("unknown");
  });
});

describe("truncated data check", () => {
  const rows = mcp({ orders: Array.from({ length: 50 }, (_, i) => ({ id: i, grand_total: 100 + i })) });

  it("fails a total stated from rows that hit the limit", () => {
    const events = call("mcp_graphjin_execute_graphql", { query: "{ orders(limit: 50) { id grand_total } }" }, { result: rows });
    const result = check("truncated_data", "Total revenue was 6,225.", events);
    expect(result.verdict).toBe("fail");
    expect(result.detail).toContain("returned 50 rows, equal to its limit 50");
  });

  it("fails an unsorted ranking from a result that reports more pages", () => {
    const events = call("graphjin_query", { query: "{ products { name } }" }, { result: mcp({ products: [{ name: "a" }], has_more: true }) });
    expect(check("truncated_data", "Product a is the best seller.", events).verdict).toBe("fail");
  });

  it("passes a sorted top-N ranking, aggregates and results under the limit", () => {
    const sorted = call("mcp_graphjin_execute_graphql", { query: "{ orders(limit: 50, order_by: { grand_total: desc }) { id } }" }, { result: rows });
    expect(check("truncated_data", "The top order is id 49.", sorted).verdict).toBe("pass");
    const aggregate = call("mcp_graphjin_execute_graphql", { query: "{ orders(limit: 1) { sum_grand_total } }" }, { result: mcp({ orders: [{ sum_grand_total: 6225 }] }) });
    expect(check("truncated_data", "Total revenue was 6,225.", aggregate).verdict).toBe("pass");
    const under = call("mcp_graphjin_execute_graphql", { query: "{ orders(limit: 100) { id } }" }, { result: rows });
    expect(check("truncated_data", "Total revenue was 6,225.", under).verdict).toBe("pass");
  });

  it("is unknown when a later query followed the truncated one", () => {
    const events = [
      ...call("mcp_graphjin_execute_graphql", { query: "{ orders(limit: 50) { id } }" }, { result: rows }),
      ...call("mcp_graphjin_execute_graphql", { query: "{ orders { count_id } }" }, { result: mcp({ orders: [{ count_id: 900 }] }) }),
    ];
    expect(check("truncated_data", "In total 900 orders.", events).verdict).toBe("unknown");
  });
});

describe("answer after failure check", () => {
  it("fails figures stated after the last data query failed", () => {
    const events = call("mcp_graphjin_execute_graphql", { query: "{ orders { grand_total } }" }, { error: "column grand_totl not found" });
    const result = check("answer_after_failure", "Revenue was 3,161,371.", events);
    expect(result.verdict).toBe("fail");
    expect(result.detail).toContain("column grand_totl not found");
  });

  it("treats GraphQL errors without data as a failure", () => {
    const events = call("mcp_graphjin_execute_graphql", { query: "{ x }" }, { result: mcp({ errors: [{ message: "bad field" }], data: null }) });
    expect(check("answer_after_failure", "Revenue was 3,161,371.", events).verdict).toBe("fail");
  });

  it("passes when figures come from an earlier result, when the answer has none, or the last call succeeded", () => {
    const events = [
      ...call("mcp_graphjin_execute_graphql", { query: "{ orders { grand_total } }" }, { result: ORDERS }),
      ...call("mcp_graphjin_execute_graphql", { query: "{ refunds { id } }" }, { error: "timeout" }),
    ];
    expect(check("answer_after_failure", "Revenue was 3,161,371.", events).verdict).toBe("pass");
    expect(check("answer_after_failure", "The refund query failed; please retry.", events).verdict).toBe("pass");
    const ok = call("mcp_graphjin_execute_graphql", { query: "{ orders { grand_total } }" }, { result: ORDERS });
    expect(check("answer_after_failure", "Revenue was 9,999,999.", ok).verdict).toBe("pass");
  });

  it("ignores non-data tools", () => {
    const events = call("read_file", { path: "SKILL.md" }, { error: "missing" });
    expect(check("answer_after_failure", "Revenue was 3,161,371.", events).verdict).toBe("unknown");
  });
});

describe("recompute comparison", () => {
  it("agrees within 1% relative or the shown precision", () => {
    expect(compareRecompute({ answerValue: "3,161,371", recomputed: 3_180_000, quantity: "total" }).verdict).toBe("agree");
    expect(compareRecompute({ answerValue: "$3.2M", recomputed: 3_161_371 }).verdict).toBe("agree");
    expect(compareRecompute({ answerValue: "12.5%", recomputed: 0.1249 }).verdict).toBe("agree");
  });

  it("disagrees outside the tolerance and reports both figures", () => {
    expect(compareRecompute({ answerValue: "3,161,371", recomputed: 3_402_118, quantity: "total" })).toEqual({
      verdict: "disagree",
      detail: "independent total 3,402,118 vs answer 3,161,371",
    });
    expect(compareRecompute({ answerValue: "3,161,371", recomputed: 3_402_118, tolerance: 0.1 }).verdict).toBe("agree");
  });

  it("is unknown without a comparable number", () => {
    expect(compareRecompute({ answerValue: "3,161,371", recomputed: null }).verdict).toBe("unknown");
    expect(compareRecompute({ answerValue: "many", recomputed: 5 }).verdict).toBe("unknown");
  });

  it("reads the value at a path or the only numeric leaf", () => {
    expect(numberAtPath({ orders: [{ sum_grand_total: 42 }] }, "orders.sum_grand_total")).toBe(42);
    expect(numberAtPath({ orders: [{ sum_grand_total: "42.5" }] }, "data.orders.0.sum_grand_total")).toBe(42.5);
    expect(numberAtPath({ a: { b: 7 } }, "missing")).toBe(7);
    expect(numberAtPath({ a: 1, b: 2 }, "missing")).toBeNull();
  });
});

describe("sampling", () => {
  it("defaults to 0.1, reads the env override and clamps it", () => {
    expect(verifyRate({})).toBe(0.1);
    expect(verifyRate({ OPENNEKO_LEARN_VERIFY_RATE: "1" })).toBe(1);
    expect(verifyRate({ OPENNEKO_LEARN_VERIFY_RATE: "3" })).toBe(1);
    expect(verifyRate({ OPENNEKO_LEARN_VERIFY_RATE: "junk" })).toBe(0.1);
  });

  it("draws a stable share of runs", () => {
    const ids = Array.from({ length: 2000 }, (_, i) => `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`);
    const share = ids.filter((id) => sampledForVerify(id, 0.1)).length / ids.length;
    expect(share).toBeGreaterThan(0.07);
    expect(share).toBeLessThan(0.13);
    expect(ids.every((id) => sampledForVerify(id, 1))).toBe(true);
    expect(ids.some((id) => sampledForVerify(id, 0))).toBe(false);
    expect(sampledForVerify(ids[5]!, 0.5)).toBe(sampledForVerify(ids[5]!, 0.5));
  });
});

describe("verification digest", () => {
  it("marks a run wrong on a failed check or a disagreeing recompute", () => {
    const rows = [
      { kind: "check" as const, checkId: "ungrounded_number", verdict: "fail" as const, detail: "number 3,161,371 not found in any data result" },
      { kind: "check" as const, checkId: "ungrounded_date", verdict: "pass" as const, detail: "ok" },
      { kind: "recompute" as const, checkId: "headline", verdict: "disagree" as const, detail: "independent total 3,402,118 vs answer 3,161,371" },
    ];
    expect(isVerifiedWrong(rows)).toBe(true);
    expect(isVerifiedWrong([{ ...rows[2]!, verdict: "agree" }])).toBe(false);
    expect(verificationLines(rows)).toEqual([
      "check failed: number 3,161,371 not found in any data result",
      "recompute disagreed: independent total 3,402,118 vs answer 3,161,371",
    ]);
  });
});

describe("evasion scan rule", () => {
  it("rejects guidance that holds back figures or dates", async () => {
    const { scanLearnedText } = await import("../src/work/skill-learn-scan");
    for (const text of [
      "Avoid stating numbers.",
      "Do not mention dates in your answer.",
      "Never give exact totals",
      "Omit the figures from the answer.",
      "Keep answers free of specific numbers.",
      "Be vague about dates.",
    ]) {
      expect(scanLearnedText(text).map((hit) => hit.code), text).toContain("evasion");
    }
  });

  it("keeps scoped data rules", async () => {
    const { scanLearnedText } = await import("../src/work/skill-learn-scan");
    for (const text of [
      "Never report totals from truncated results.",
      "Do not double count totals across currencies.",
      "Count orders without dates separately.",
      "Omit the totals row before summing.",
      "State the time window you used.",
    ]) {
      expect(scanLearnedText(text), text).toEqual([]);
    }
  });

  it("rejects a whole proposal that adds such a rule", async () => {
    const { applyPlaybookOperations } = await import("../src/work/skill-learn-playbook");
    const update = applyPlaybookOperations({
      current: [],
      operations: [{ op: "add", text: "Avoid stating numbers.", evidenceRunIds: [], evidenceQuotes: [] }],
      evidence: [],
    });
    expect(update.unsafe).toHaveLength(1);
    expect(update.rules).toEqual([]);
  });
});

describe("verified-wrong runs in the learner", () => {
  const failedCheck = [
    { kind: "check" as const, checkId: "ungrounded_number", verdict: "fail" as const, detail: "number 3,161,371 not found in any data result" },
  ];
  const disagreed = [
    { kind: "recompute" as const, checkId: "headline", verdict: "disagree" as const, detail: "independent total 3,402,118 vs answer 3,161,371" },
  ];

  async function runs() {
    const { buildRunEvidence } = await import("../src/work/skill-learn-evidence");
    const make = (runId: string, verifications: Parameters<typeof buildRunEvidence>[0]["verifications"] = []) =>
      buildRunEvidence({ runId, status: "completed", request: "revenue?", answer: "Revenue was 3,161,371.", events: [], verifications });
    return { make };
  }

  it("puts verification lines in the digest and marks the run wrong", async () => {
    const { make } = await runs();
    const run = make("r1", [...failedCheck, ...disagreed]);
    expect(run.verifiedWrong).toBe(true);
    expect(run.digest).toContain("run r1: outcome completed, tool errors 0, failed verification");
    expect(run.digest).toContain("check failed: number 3,161,371 not found in any data result");
    expect(run.digest).toContain("recompute disagreed: independent total 3,402,118 vs answer 3,161,371");
    const pending = make("r2", [{ kind: "recompute", checkId: "headline", verdict: "pending", detail: "" }]);
    expect(pending.verifiedWrong).toBeUndefined();
    expect(pending.digest).not.toContain("recompute");
  });

  it("reserves sample slots for verified-wrong runs and counts them", async () => {
    const { make } = await runs();
    const { countOutcomes, sampleEvidence } = await import("../src/work/skill-learn");
    const clean = Array.from({ length: 12 }, (_, i) => make(`ok${i}`));
    const wrong = [make("bad1", failedCheck), make("bad2", disagreed)];
    const picked = sampleEvidence([...clean, ...wrong], 4);
    expect(picked.map((run) => run.runId)).toEqual(expect.arrayContaining(["bad1", "bad2"]));
    expect(countOutcomes([...clean.slice(0, 3), ...wrong])).toEqual({ completed: 5, verified_wrong: 2 });
  });

  it("counts verified-wrong runs as failures in the version and rule guards", async () => {
    const { make } = await runs();
    const { citedRulesToRollBack, toScoreSample } = await import("../src/work/learn-target-store");
    const { skillScoreSample } = await import("../src/work/skill-learn-store");
    const { learnedVersionRegressed, scoreSkillLearnWindow } = await import("../src/work/skill-learn-score");
    expect(toScoreSample(make("x", failedCheck)).hadRelevantFailure).toBe(true);
    expect(skillScoreSample("s")(make("y", disagreed)).hadRelevantFailure).toBe(true);
    expect(toScoreSample(make("z")).hadRelevantFailure).toBe(false);

    const before = scoreSkillLearnWindow(Array.from({ length: 5 }, (_, i) => toScoreSample(make(`b${i}`))));
    const after = scoreSkillLearnWindow(Array.from({ length: 5 }, (_, i) => toScoreSample(make(`a${i}`, i < 2 ? failedCheck : []))));
    expect(learnedVersionRegressed(before, after)).toBe(true);

    const target = { kind: "source" as const, id: "src" };
    const rule = { id: "L1", text: "Join through sales_order_detail.", evidenceRunIds: [], evidenceQuotes: [] };
    const cited = ["c1", "c2", "c3"].map((id) => make(id, failedCheck));
    const others = ["o1", "o2", "o3"].map((id) => make(id));
    const culprits = citedRulesToRollBack({
      target,
      rules: [rule],
      evidence: [...cited, ...others],
      citations: cited.map((run) => ({ runId: run.runId, ruleKey: "source:src:L1" })),
    });
    expect(culprits.map((culprit) => culprit.rule.id)).toEqual(["L1"]);
  });

  it("tells the proposer that verification failures mark a run wrong", async () => {
    const { buildSkillLearnPrompt, buildTargetLearnPrompt } = await import("../src/work/skill-learn-propose");
    const skill = buildSkillLearnPrompt({ skillName: "s", skillMarkdown: "", rules: [], evidence: [] });
    const source = buildTargetLearnPrompt({ kind: "source", label: "db", rules: [], evidence: [], maxRules: 8 });
    for (const prompt of [skill, source]) {
      expect(prompt).toContain('A "check failed" line or a "recompute disagreed" line also means that run was wrong');
    }
  });
});
