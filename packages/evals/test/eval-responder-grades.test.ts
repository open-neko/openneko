import { describe, expect, it } from "vitest";
import {
  gradeCards,
  gradeClosingBlocks,
  gradeNoLeak,
  responderMeasurements,
  type ResponderGradeInput,
} from "../scripts/eval-responder-grades";

const good: ResponderGradeInput = {
  events: [
    { type: "message", role: "assistant", content: "Current value: 7.2%" },
    { type: "followups", items: ["Break this down by region"] },
    { type: "vitals", items: [{ label: "Tax rate", value: "7.2%" }] },
  ],
  finalText: "Current value: 7.2%\nCurrent window: 2013-07-01 to 2014-06-30",
  question: "What was our effective sales tax rate?",
  workflowRun: false,
  cardsEnabled: false,
  analysisMinutesSaved: 20,
};

describe("closing blocks grader", () => {
  it("passes when the three blocks parsed", () => {
    expect(gradeClosingBlocks(good)).toEqual({ due: true, passed: true, findings: [] });
  });

  it("passes an empty vitals list, which the prompt allows", () => {
    const events = [...good.events.slice(0, 2), { type: "vitals", items: [] }];
    expect(gradeClosingBlocks({ ...good, events }).passed).toBe(true);
  });

  it("fails each missing block by name", () => {
    expect(gradeClosingBlocks({ ...good, analysisMinutesSaved: null }).findings).toEqual(["neko_value"]);
    expect(
      gradeClosingBlocks({ ...good, events: good.events.filter((event) => event.type !== "vitals") }).findings,
    ).toEqual(["neko_vitals"]);
    expect(
      gradeClosingBlocks({ ...good, events: good.events.filter((event) => event.type !== "followups") }).findings,
    ).toEqual(["neko_followups"]);
    expect(gradeClosingBlocks({ ...good, events: [], analysisMinutesSaved: undefined })).toEqual({
      due: true,
      passed: false,
      findings: ["neko_value", "neko_vitals", "neko_followups"],
    });
  });

  it("asks only for the time-saved block on a workflow run", () => {
    expect(gradeClosingBlocks({ ...good, events: [], workflowRun: true }).passed).toBe(true);
    expect(gradeClosingBlocks({ ...good, events: [], workflowRun: true, analysisMinutesSaved: null }).findings).toEqual([
      "neko_value",
    ]);
  });

  it("is not due after a clarification question", () => {
    const events = [{ type: "needs_input", question: "Which year?" }];
    expect(gradeClosingBlocks({ ...good, events, analysisMinutesSaved: null }).due).toBe(false);
  });
});

describe("cards grader", () => {
  const chart = { ...good, cardsEnabled: true, question: "Show monthly revenue as a chart." };

  it("is due only when cards are on and the question asks for a chart or table", () => {
    expect(gradeCards(good).due).toBe(false);
    expect(gradeCards({ ...chart, cardsEnabled: false }).due).toBe(false);
    expect(gradeCards({ ...good, cardsEnabled: true }).due).toBe(false);
    expect(gradeCards({ ...chart, question: "List the top vendors in a table" }).due).toBe(true);
  });

  it("passes with a card surface and fails without one", () => {
    const surface = { type: "surface", messages: [{ surfaceUpdate: { components: [] } }] };
    expect(gradeCards({ ...chart, events: [...good.events, surface] }).passed).toBe(true);
    expect(gradeCards(chart)).toEqual({ due: true, passed: false, findings: ["no card surface"] });
    expect(gradeCards({ ...chart, events: [{ type: "surface", messages: [] }] }).passed).toBe(false);
  });
});

describe("leak grader", () => {
  it("passes the closing blocks, which the client hides", () => {
    const finalText =
      "Receipt AW-1.\n\n```neko_value\n{ \"minutes_saved\": 20 }\n```\n\n```neko_vitals\n{ \"vitals\": [] }\n```\n\n```neko_followups\n{ \"followups\": [\"Next\"] }\n```";
    expect(gradeNoLeak({ ...good, finalText })).toEqual({ due: true, passed: true, findings: [] });
  });

  it("passes a plain answer and a closed SQL block", () => {
    expect(gradeNoLeak(good).passed).toBe(true);
    expect(gradeNoLeak({ ...good, finalText: "Query:\n```sql\nselect 1\n```\nCurrent value: 7.2%" }).passed).toBe(true);
  });

  it.each([
    ["an unclosed fence", "Current value: 7.2%\n```neko", "unclosed fence"],
    ["a machine block name", 'Current value: 7.2%\nneko_vitals { "vitals": [] }', "machine block name"],
    ["an unclosed closing block", 'Current value: 7.2%\n```neko_vitals\n{ "vitals": [', "machine block name"],
    ["an executor code fence", "```javascript\nconst rows = 1;\n```", "code fence"],
    ["a final() call", 'final("Current value: 7.2%", {})', "executor code"],
    ["an awaited tool call", "const r = await tools.graphjin({ query })", "executor code"],
    ["an executor program object", '{"javascriptCode":"final(1)"}', "executor code"],
  ])("fails %s", (_name, finalText, finding) => {
    const grade = gradeNoLeak({ ...good, finalText });
    expect(grade.passed).toBe(false);
    expect(grade.findings).toContain(finding);
  });
});

describe("responder measurements", () => {
  it("sums the streamed requests only and names the trial effort", () => {
    const requests = [
      { runId: "r", callId: 1, streamed: false, durationMs: 3000, usage: { input_tokens: 900, output_tokens: 40 } },
      {
        runId: "r",
        callId: 2,
        streamed: true,
        durationMs: 5200,
        model: "gemini-3.8-flash",
        responderReasoningEffort: "low",
        usage: { input_tokens: 1200, output_tokens: 300, reasoning_tokens: 80 },
      },
    ];
    expect(responderMeasurements(requests)).toEqual({
      modelRequests: 2,
      responderCalls: 1,
      responderLatencyMs: 5200,
      responderInputTokens: 1200,
      responderOutputTokens: 300,
      responderReasoningTokens: 80,
      responderUsageReported: true,
      responderModels: ["gemini-3.8-flash"],
      responderReasoningEffort: "low",
    });
    expect(responderMeasurements([requests[0]!])).toEqual({ modelRequests: 1, responderCalls: 0 });
    expect(responderMeasurements([{ runId: "r", callId: 3, streamed: true, durationMs: 4100 }])).toEqual({
      modelRequests: 1,
      responderCalls: 1,
      responderLatencyMs: 4100,
      responderUsageReported: false,
      responderModels: [undefined],
    });
  });

  it("does not count a stream that ended early as reported", () => {
    const partial = {
      runId: "r",
      callId: 4,
      streamed: true,
      durationMs: 900,
      usage: { input_tokens: 1200, output_tokens: 12, coverage: "partial" },
    };
    expect(responderMeasurements([partial])).toEqual({
      modelRequests: 1,
      responderCalls: 1,
      responderLatencyMs: 900,
      responderUsageReported: false,
      responderModels: [undefined],
    });
  });
});
