import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import PackChoices from "../src/app/admin/rules/PackChoices";

it("asks ask or auto once with the line on where the search text goes", () => {
  const html = renderToStaticMarkup(createElement(PackChoices, {
    choices: [{ policyId: "p1", packId: "parallel-search", question: "Parallel web search runs each search with no approval.",
      line: "Each search sends its search text out of this install to Parallel. Choose whether a person approves each search." }],
    onDecided: () => undefined,
  }));
  for (const text of ["A pack rule needs your choice", "runs each search with no approval", "out of this install to Parallel"]) {
    expect(html).toContain(text);
  }
  expect(html).toMatch(/<button[^>]*>Ask before each search<\/button>/);
  expect(html).toMatch(/<button[^>]*>Keep auto<\/button>/);
});

it("shows nothing when no choice waits", () => {
  expect(renderToStaticMarkup(createElement(PackChoices, { choices: [], onDecided: () => undefined }))).toBe("");
});
