import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import OwnerlessWorkflows from "../src/app/admin/settings/workflows/OwnerlessWorkflows";

const users = [{ id: "u1", email: "ana@example.com", name: "Ana" }, { id: "u2", email: "bo@example.com", name: null }];

it("lists each workflow with no owner on one line with an owner choice", () => {
  const html = renderToStaticMarkup(createElement(OwnerlessWorkflows, {
    workflows: [{ id: "w1", name: "Daily Revenue Health Check" }, { id: "w2", name: "Stock Reorder Watch" }],
    users,
  }));
  expect(html).toContain("Workflows with no owner");
  expect(html.match(/<li>/g)).toHaveLength(2);
  expect(html).toMatch(/<label[^>]*for="owner-w1"[^>]*>Daily Revenue Health Check<\/label>/);
  expect(html).toContain("Daily Revenue Health Check");
  expect(html).toContain('<option value="u1">Ana (ana@example.com)</option>');
  expect(html).toContain('<option value="u2">bo@example.com</option>');
  expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Assign<\/button>/);
});

it("shows nothing when every workflow has an owner", () => {
  expect(renderToStaticMarkup(createElement(OwnerlessWorkflows, { workflows: [], users }))).toBe("");
});
