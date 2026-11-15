import { describe, expect, it } from "vitest";
import { coalesceRunEvents, type RunEventRow } from "@/lib/run-event-items";

function row(seq: number, type: string, event: Record<string, unknown> | null = null): RunEventRow {
  return { seq, type, event, createdAt: "2026-11-15T00:00:00Z" };
}

describe("coalesceRunEvents", () => {
  it("joins consecutive message rows into one block", () => {
    const items = coalesceRunEvents([
      row(1, "message", { content: "**Daily " }),
      row(2, "message", { content: "check**" }),
      row(3, "status", { status: "done" }),
    ]);
    expect(items).toEqual([
      { type: "message-block", seq: 1, content: "**Daily check**" },
      { type: "single", seq: 3, ev: row(3, "status", { status: "done" }) },
    ]);
  });

  it("keeps only the answer after the last reset", () => {
    const items = coalesceRunEvents([
      row(1, "message", { content: "First attempt." }),
      row(2, "tool", { name: "query" }),
      row(3, "message", { content: " More of it." }),
      row(4, "answer_reset"),
      row(5, "message", { content: "Second attempt." }),
    ]);
    const blocks = items.filter((item) => item.type === "message-block");
    expect(blocks).toEqual([{ type: "message-block", seq: 5, content: "Second attempt." }]);
    expect(items.map((item) => item.seq)).toEqual([2, 5]);
  });
});
