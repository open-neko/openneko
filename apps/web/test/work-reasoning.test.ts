import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { buildRunTimeline } from "../src/app/(work)/work/work-timeline-model";
import type { WorkEvent } from "../src/app/(work)/work/work-types";
import {
  formatElapsed,
  liveStep,
  progressPreview,
  ProviderProgress,
  WorkTrail,
} from "../src/app/(work)/work/work-progress";

const run = {
  id: "run-1",
  backend: "ax" as const,
  status: "completed",
  error: null,
  createdAt: "2026-10-05T09:00:00Z",
  finishedAt: "2026-10-05T09:03:12Z",
};

describe("work reasoning", () => {
  it("previews a note by its first line without markdown markers", () => {
    expect(progressPreview("**Locating the view**\n\nI check the catalog.")).toBe("Locating the view");
    expect(progressPreview("I should load the skill first.")).toBe("I should load the skill first.");
    expect(progressPreview(`# ${"x".repeat(200)}`)).toHaveLength(140);
  });

  it("formats elapsed time in seconds, minutes and hours", () => {
    expect(formatElapsed(45_000)).toBe("45 s");
    expect(formatElapsed(192_000)).toBe("3 min 12 s");
    expect(formatElapsed(180_000)).toBe("3 min");
    expect(formatElapsed(3_900_000)).toBe("1 h 5 min");
  });

  it("renders a Gemini note with headings as one note, the same as a plain note", () => {
    const gemini = renderToStaticMarkup(createElement(ProviderProgress, { content: "**Locating the view**\n\nI check the catalog.\n\n**Checking rows**\n\nTwo rows.", live: false }));
    const plain = renderToStaticMarkup(createElement(ProviderProgress, { content: "I check the catalog.", live: false }));
    for (const html of [gemini, plain]) {
      expect(html).toContain("work-progress-summary");
      expect(html).not.toContain("<details");
    }
    expect(gemini).toContain("Checking rows");
  });

  it("collapses an earlier note to its preview", () => {
    const html = renderToStaticMarkup(createElement(ProviderProgress, { content: "**Locating the view**\n\nDetail.", live: false, collapsed: true }));
    expect(html).toContain("<details");
    expect(html).not.toMatch(/<details[^>]*\sopen[\s>=]/);
    expect(html).toContain("Locating the view");
  });

  it("folds a finished trail into one row with its duration and counts", () => {
    const html = renderToStaticMarkup(createElement(WorkTrail, {
      run,
      items: [
        { kind: "progress", id: "p1", content: "a" },
        { kind: "interim", id: "i1", content: "b" },
        { kind: "tools", tools: [{}, {}, {}] as never },
      ],
    }, createElement("p", null, "trail")));
    expect(html).toContain("Worked for 3 min 12 s");
    expect(html).toContain("2 notes, 3 tool calls");
  });

  it("renders the same thinking text the same way for every provider", () => {
    const deltas = ["**Counting the orders**\n\nThe query ", "returned one row.", "\n\n**Writing the answer**\n\nOne sentence."];
    const render = (provider: string) => {
      const events: WorkEvent[] = [
        { type: "status", message: "Check the data" },
        ...deltas.map((content): WorkEvent => ({ type: "progress", id: "ax-summary-1", content, source: "provider_summary", provider })),
      ];
      const { items, lastStatus } = buildRunTimeline(events, "run-1");
      const notes = items.filter((item) => item.kind === "progress");
      expect(notes).toEqual([{ kind: "progress", id: "ax-summary-1", content: deltas.join("") }]);
      return {
        open: renderToStaticMarkup(createElement(ProviderProgress, { content: notes[0].content, live: true })),
        collapsed: renderToStaticMarkup(createElement(ProviderProgress, { content: notes[0].content, live: false, collapsed: true })),
        step: liveStep(items, lastStatus),
      };
    };
    const anthropic = render("anthropic");
    for (const provider of ["google-gemini", "deepseek", "openrouter", "together"]) {
      expect(render(provider)).toEqual(anthropic);
    }
    expect(anthropic.open).toContain("Writing the answer");
    expect(anthropic.step).toBe("Counting the orders");
  });

  it("shows the step status when a provider sends no thinking text", () => {
    const { items, lastStatus } = buildRunTimeline([{ type: "status", message: "Check the data" }], "run-1");
    expect(liveStep(items, lastStatus)).toBe("Check the data");
    expect(liveStep([], null)).toBeNull();
  });
});
