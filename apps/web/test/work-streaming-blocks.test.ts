import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RunTimeline } from "../src/app/(work)/work/work-run-timeline";
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

  it("gives the same result at each growth step as a parse of the whole text", () => {
    const text = [
      "Sales rose by 4% in March. See [the report](https://openneko.app/r).",
      "- East led\n- West fell\n\n    ```sql\n    select 1\n    ```",
      "> | Region | Sales |\n> | --- | --- |\n> | East | 12 |",
      "Region | Sales\n--- | ---\nEast | 12",
      "~~~\nraw\n~~~",
      "```neko_value\n{\"value\": 1}\n```",
      "Done.",
    ].join("\n\n");
    const prefixes = Array.from({ length: text.length }, (_, i) => text.slice(0, i + 1));
    const whole = [...prefixes].reverse().map(holdOpenBlock).reverse();
    expect(prefixes.map(holdOpenBlock)).toEqual(whole);
  });

  it.each([
    ["a fence indented four spaces in a nested list", "- Step one\n\n    ```sql\n    select 1\n", "- Step one\n\n", "code"],
    ["a fence in a blockquote", "Note:\n\n> ```sql\n> select 1\n", "Note:\n\n", "code"],
    ["a tilde fence", "Run this:\n~~~sql\nselect 1\n", "Run this:\n", "code"],
    ["a table in a blockquote", "Sales:\n\n> | Region | Sales |\n> | --- | --- |\n> | East | 12 |\n", "Sales:\n\n", "table"],
    ["a table with no leading pipe", "Sales:\n\nRegion | Sales\n--- | ---\nEast | 12", "Sales:\n\n", "table"],
    ["a header with no leading pipe", "Sales:\n\nRegion | Sales", "Sales:\n\n", "table"],
    ["a header over a part delimiter", "Sales:\n\nRegion | Sales\n---", "Sales:\n\n", "table"],
    ["a half-written link", "See [the sales report](https://openneko.app/re", "See ", null],
    ["a half-written image", "Chart:\n\n![Sales by region](https://openn", "Chart:\n\n", null],
    ["a link with no target yet", "See [the sales report", "See ", null],
  ] as const)("holds %s", (_name, text, shown, held) => {
    expect(holdOpenBlock(text)).toEqual({ shown, held });
  });

  it.each([
    ["a closed tilde fence", "Run this:\n~~~sql\nselect 1\n~~~\nDone."],
    ["a closed fence in a blockquote", "> ```sql\n> select 1\n> ```\n\nDone."],
    ["a table in a blockquote with a blank line after it", "> | Region | Sales |\n> | --- | --- |\n> | East | 12 |\n>\n"],
    ["a table with no leading pipe and a blank line after it", "Region | Sales\n--- | ---\nEast | 12\n\n"],
    ["a line that starts with inline code of three backticks", "```npm ci``` installs the packages.\nThen run the tests and read the result."],
    ["a complete link", "See [the sales report](https://openneko.app/report)"],
    ["a link mark inside inline code", "Write `[x` to open a list."],
    ["a heading over a rule with no pipe", "Summary\n---\nSales rose."],
  ] as const)("shows %s", (_name, text) => {
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

  it("removes every answer text of the run on a reset after a tool call", () => {
    const events: WorkEvent[] = [
      message("Revenue fell by 4%."),
      { type: "card_pending" },
      { type: "tool_start", id: "t1", name: "query" },
      { type: "tool_end", id: "t1", result: { ok: true } },
      message("Checking again."),
      { type: "answer_reset" },
      message("Revenue rose by 2%."),
      { type: "done" },
    ];
    const items = buildRunTimeline(events, "run-1").items;
    expect(items.map((item) => item.kind)).toEqual(["tools", "text"]);
    expect(items.filter((item) => item.kind === "text")).toEqual([{ kind: "text", content: "Revenue rose by 2%." }]);
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

describe("the streamed answer render", () => {
  const run = { id: "run-1", backend: "ax" as const, status: "running", error: null, createdAt: "2026-11-15T09:00:00Z", finishedAt: null };
  const render = (events: WorkEvent[], pending: boolean) =>
    renderToStaticMarkup(createElement(RunTimeline, {
      threadId: "t",
      run,
      events,
      pending,
      fallbackContent: "",
      reasoningExpanded: false,
    }));

  it("holds a half table when a thinking note follows it", () => {
    const events: WorkEvent[] = [
      message("Sales by region:\n\n| Region | Sales |\n| --- | --- |\n| East | 12 |\n"),
      { type: "progress", id: "p1", content: "Checking the west.", source: "provider_summary", provider: "google-gemini" },
    ];
    const live = render(events, true);
    expect(live).toContain("Sales by region:");
    expect(live).not.toContain("<table");
    expect(live).not.toContain("| Region");
    expect(render(events, false)).toContain("<table");
  });

  it("shows a table that a tool call ended", () => {
    const events: WorkEvent[] = [
      message("| Region | Sales |\n| --- | --- |\n| East | 12 |"),
      { type: "tool_start", id: "t1", name: "query" },
      message("Checking the west."),
    ];
    expect(render(events, true)).toContain("<table");
  });
});
