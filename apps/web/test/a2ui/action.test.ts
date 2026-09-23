import { describe, expect, it } from "vitest";
import { buildActionFollowUp, parseClarificationReply } from "@/a2ui/action";

describe("buildActionFollowUp", () => {
  it("keeps legacy prompt-only actions compact", () => {
    expect(buildActionFollowUp({ prompt: "Show the trend." })).toBe("Show the trend.");
  });

  it("makes submitted configuration visible in the next operator turn", () => {
    const result = buildActionFollowUp({
      prompt: "Review and file a source proposal.",
      values: { name: "warehouse", port: 5432, secretRef: "warehouse-password" },
    });
    expect(result).toContain("Review and file a source proposal.");
    expect(result).toContain('"name": "warehouse"');
    expect(result).toContain('"secretRef": "warehouse-password"');
  });

  it("requires an explicit action prompt", () => {
    expect(buildActionFollowUp({ values: { name: "warehouse" } })).toBeNull();
  });
});

it("shows clarification answers without changing the submitted message", () => {
  const message = buildActionFollowUp({
    prompt: "Continue the previous request using these operator-supplied answers. Re-check any remaining external facts and do not infer omitted values.",
    questions: [
      { id: "q1", header: "Region", question: "Which region?" },
      { id: "q2", question: "Any notes?" },
    ],
    answers: { q1: ["UAE"], q2: "Use the current catalog." },
  });
  expect(message).toContain('"answers"');
  expect(parseClarificationReply(message!)).toEqual([
    { question: "Region: Which region?", answer: "UAE" },
    { question: "Any notes?", answer: "Use the current catalog." },
  ]);
  expect(parseClarificationReply("A regular message with JSON")).toBeNull();
});
