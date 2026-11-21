import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import HeldWorkflows from "../src/app/admin/settings/workflows/HeldWorkflows";

it("lists each changed workflow with a link to read it and an approve button", () => {
  const html = renderToStaticMarkup(createElement(HeldWorkflows, {
    workflows: [{ id: "w1", name: "Order intake" }, { id: "w2", name: "Refund check" }],
  }));
  expect(html).toContain("Changed workflows with API access");
  expect(html.match(/<li/g)).toHaveLength(2);
  expect(html).toContain('href="/workflows?id=w1"');
  expect(html).toMatch(/<button[^>]*>Approve<\/button>/);
});

it("shows nothing when no workflow waits", () => {
  expect(renderToStaticMarkup(createElement(HeldWorkflows, { workflows: [] }))).toBe("");
});
