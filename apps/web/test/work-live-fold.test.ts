import { describe, expect, it } from "vitest";
import { buildRunTimeline, liveFoldIndexes } from "../src/app/(work)/work/work-timeline-model";
import type { WorkEvent } from "../src/app/(work)/work/work-types";

const sql = "mcp_neko_graphjin_execute_graphql";

const note = (id: string, content: string): WorkEvent => ({
  type: "progress",
  id,
  content,
  source: "provider_summary",
  provider: "gemini",
});

function rounds(count: number): WorkEvent[] {
  return Array.from({ length: count }, (_, i): WorkEvent[] => [
    note(`note-${i}`, `Checking region ${i}`),
    { type: "tool_start", id: `tool-${i}`, name: sql, label: "Reading sales orders" },
    { type: "tool_end", id: `tool-${i}` },
  ]).flat();
}

const folded = (events: WorkEvent[]) => {
  const { items } = buildRunTimeline(events, "run-1");
  return [...liveFoldIndexes(items)].map((i) => items[i].kind);
};

describe("the fold of a running answer", () => {
  it("keeps a short run open", () => {
    expect(folded(rounds(1))).toEqual([]);
    expect(folded([
      note("a", "Reading the question"),
      note("b", "Comparing"),
    ])).toEqual([]);
  });

  it("folds every earlier note and step and keeps the newest step with its note", () => {
    expect(folded(rounds(7))).toEqual(Array.from({ length: 6 }, () => ["progress", "tools"]).flat());
  });

  it("keeps only the newest note when no step follows it", () => {
    const kinds = folded([...rounds(3), note("next", "Comparing")]);
    expect(kinds).toEqual(["progress", "tools", "progress", "tools", "progress", "tools"]);
  });

  it("leaves the answer text and the cards out of the fold", () => {
    const { items } = buildRunTimeline(
      [...rounds(4), { type: "message", role: "assistant", content: "Sales held steady." }],
      "run-1",
    );
    const fold = liveFoldIndexes(items);
    expect([...fold].every((i) => items[i].kind !== "text")).toBe(true);
    expect(fold.size).toBe(6);
  });
});
