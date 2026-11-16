import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { buildRunTimeline } from "../src/app/(work)/work/work-timeline-model";
import type { ToolItem, WorkEvent } from "../src/app/(work)/work/work-types";
import { liveStep } from "../src/app/(work)/work/work-progress";
import { ToolGroup } from "../src/app/(work)/work/work-tool-rows";

const sql = "mcp_neko_graphjin_execute_graphql";

function tools(events: WorkEvent[]): ToolItem[] {
  const item = buildRunTimeline(events, "run-1").items.find((i) => i.kind === "tools");
  if (item?.kind !== "tools") throw new Error("no tool group");
  return item.tools;
}

const render = (list: ToolItem[]) =>
  renderToStaticMarkup(createElement(ToolGroup, { tools: list, threadId: "t", runId: "run-1" }));

describe("tool steps in plain words", () => {
  it("shows the running call's label in place of a tool count", () => {
    const list = tools([
      { type: "tool_start", id: "1", name: sql, label: "Checking the shape of the data" },
      { type: "tool_end", id: "1" },
      { type: "tool_start", id: "2", name: sql, label: "Reading sales orders, filtered by order date" },
    ]);
    const html = render(list);
    expect(html).toContain("Reading sales orders, filtered by order date");
    expect(html).toContain("2 steps");
    expect(html).not.toContain("tool call");
    expect(html).not.toContain(sql);
  });

  it("says a failed call in words, not as a red count", () => {
    const list = tools([
      { type: "tool_start", id: "1", name: sql, label: "Reading orders" },
      { type: "tool_end", id: "1", error: `${sql}_failed`, failed: true, label: "That query failed; trying another way" },
      { type: "tool_start", id: "2", name: "mcp_neko_memory_search", label: "Searching saved memories" },
      { type: "tool_end", id: "2", failed: true },
    ]);
    const html = render(list);
    expect(html).toContain("work-tool-group-step-failed");
    expect(html).toContain("That step failed; trying another way");
    expect(html).not.toMatch(/\d+ failed/);
  });

  it("names a stored call with no label by its tool, without the transport prefix", () => {
    const html = render(tools([{ type: "tool_start", id: "1", name: sql }, { type: "tool_end", id: "1" }]));
    expect(html).toContain("Graphjin execute graphql");
    expect(html).not.toContain("mcp_neko");
  });

  it("puts the newest step on the open line while the run works", () => {
    const { items, lastStatus } = buildRunTimeline([
      { type: "status", message: "Check the data" },
      { type: "progress", id: "p1", content: "**Planning**\n\nFirst the orders." },
      { type: "tool_start", id: "1", name: sql, label: "Reading sales orders" },
    ], "run-1");
    expect(liveStep(items, lastStatus)).toBe("Reading sales orders");
  });
});
