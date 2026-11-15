import { describe, expect, it } from "vitest";
import { holdOpenBlock } from "../src/app/(work)/work/work-streaming-text";
import { buildRunTimeline } from "../src/app/(work)/work/work-timeline-model";
import { applyStreamEventToBundle, streamedAnswerText } from "../src/app/(work)/work/work-thread";
import type { ThreadBundle, WorkEvent } from "../src/app/(work)/work/work-types";

const TABLE = "| Region | Sales |\n| --- | ---: |\n| East | 12 |\n| West | 9 |";

describe("holdOpenBlock", () => {
  it("shows plain prose as it arrives", () => {
    expect(holdOpenBlock("Sales rose by 4% in Mar")).toEqual({ shown: "Sales rose by 4% in Mar", held: null });
  });

  it("holds every growth step of a table until a line follows it", () => {
    const text = `Sales by region:\n\n${TABLE}\n\nEast led.`;
    const cut = text.indexOf("| Region");
    for (let end = cut + 1; end <= text.indexOf("\n\nEast led."); end++) {
      expect(holdOpenBlock(text.slice(0, end))).toEqual({ shown: text.slice(0, cut), held: "table" });
    }
    expect(holdOpenBlock(`${text.slice(0, cut)}${TABLE}\n\n`)).toEqual({ shown: `${text.slice(0, cut)}${TABLE}\n\n`, held: null });
    expect(holdOpenBlock(text)).toEqual({ shown: text, held: null });
  });

  it("holds an open code fence and shows it once closed", () => {
    const text = "Run this:\n```sql\nselect 1\n```\nDone.";
    const cut = "Run this:\n".length;
    for (let end = cut + 4; end < text.indexOf("\n```\nDone") + 3; end++) {
      expect(holdOpenBlock(text.slice(0, end)).shown).toBe(text.slice(0, cut));
    }
    expect(holdOpenBlock("Run this:\n```sql\nselect 1\n")).toEqual({ shown: "Run this:\n", held: "code" });
    expect(holdOpenBlock(text)).toEqual({ shown: text, held: null });
  });

  it("never shows raw fence marks of a hidden block", () => {
    expect(holdOpenBlock("There are 31,465.\n`")).toEqual({ shown: "There are 31,465.\n", held: null });
    expect(holdOpenBlock("There are 31,465.\n``")).toEqual({ shown: "There are 31,465.\n", held: null });
    expect(holdOpenBlock("There are 31,465.\n```neko")).toEqual({ shown: "There are 31,465.\n", held: null });
    expect(holdOpenBlock('There are 31,465.\n```neko_value\n{"value":')).toEqual({ shown: "There are 31,465.\n", held: null });
  });

  it("ignores table marks inside a closed code block", () => {
    const text = "```\n| a |\n```\nAfter.";
    expect(holdOpenBlock(text)).toEqual({ shown: text, held: null });
  });
});

function message(content: string): WorkEvent {
  return { type: "message", role: "assistant", content };
}

describe("the streamed answer timeline", () => {
  it("replaces, not doubles, the text when the model starts its answer again", () => {
    const events: WorkEvent[] = [
      message("Revenue fell "),
      message("by 4%."),
      { type: "progress", id: "p1", content: "Checking again.", source: "provider_summary", provider: "google-gemini" },
      { type: "answer_reset" },
      message("Revenue rose by 2%."),
    ];
    const items = buildRunTimeline(events, "run-1").items;
    expect(items.filter((item) => item.kind === "text")).toEqual([{ kind: "text", content: "Revenue rose by 2%." }]);
    expect(items.map((item) => item.kind)).toEqual(["progress", "text"]);
    expect(streamedAnswerText(events)).toBe("Revenue rose by 2%.");
  });

  it("places a card placeholder after the prose and removes it when the card arrives", () => {
    const events: WorkEvent[] = [message("Sales rose."), { type: "card_pending" }, message("East led.")];
    expect(buildRunTimeline(events, "run-1").items.map((item) => item.kind)).toEqual(["text", "card_pending", "text"]);
    const done = buildRunTimeline([...events, { type: "surface", messages: [] }], "run-1").items;
    expect(done.map((item) => item.kind)).toEqual(["text", "text", "surface"]);
  });

  it("clears the live assistant message on a reset", () => {
    const bundle = {
      thread: { id: "t", title: "", createdAt: "", updatedAt: "", lastMessageAt: "" },
      runs: [],
      messages: [{ id: "m", runId: "run-1", role: "assistant", content: "Old", createdAt: "" }],
      eventsByRun: {},
    } as unknown as ThreadBundle;
    const reset = applyStreamEventToBundle(bundle, "run-1", { type: "answer_reset" });
    const next = applyStreamEventToBundle(reset, "run-1", message("New"));
    expect(next.messages[0].content).toBe("New");
  });
});
