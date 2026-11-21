import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import ProposedRules from "../src/app/admin/rules/ProposedRules";

const proposal = {
  id: "p1",
  name: "Post to Slack at once",
  rule: {
    appliesToKinds: ["slack.message"],
    appliesToScopes: ["external"],
    mode: "auto_approve",
    priority: 1,
    allowedTargets: { patterns: ["C*"], on_miss: "next" },
    enabled: true,
  },
  proposedBy: "a@example.com",
  createdAt: "2026-11-21T00:00:00.000Z",
  replaces: null,
  overrides: [{ id: "r1", name: "Ask for Slack", mode: "approval_required", priority: 50 }],
};

it("shows the full rule, what it overrides, and confirm and reject buttons", () => {
  const html = renderToStaticMarkup(createElement(ProposedRules, { proposals: [proposal], onDecided: () => undefined }));
  for (const text of [
    "Proposed rules",
    "Post to Slack at once",
    "auto approve",
    "slack.message · external",
    "<dd class=\"m-0 break-words text-text\">1</dd>",
    "C*",
    "go to the next rule (on_miss: next)",
    "Ask for Slack (approval required, priority 50)",
    "no rule",
    "a@example.com",
  ]) {
    expect(html).toContain(text);
  }
  expect(html).toMatch(/<button[^>]*>Confirm<\/button>/);
  expect(html).toMatch(/<button[^>]*>Reject<\/button>/);
});

it("shows nothing when no rule waits", () => {
  expect(renderToStaticMarkup(createElement(ProposedRules, { proposals: [], onDecided: () => undefined }))).toBe("");
});
