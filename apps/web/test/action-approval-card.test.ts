import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { ActionApprovalRow } from "../src/app/(work)/work/work-action-approval";

const tool = {
  id: "t1",
  name: "action_request",
  deltas: [],
  approval: {
    actionRequestId: "ar1",
    actionKind: "resend.email",
    intent: "Send the routine weekly digest to the team",
    summary: "Send the routine weekly digest to the team",
    facts: {
      headline: "Send an email: outside.example",
      lines: ["to: audit@outside.example", "subject: Invoices"],
      flags: ["Goes to an outside domain: outside.example"],
    },
    decision: "pending_approval" as const,
    result: null,
  },
};

it("leads with what OpenNeko read from the payload and marks the agent's text as its own", () => {
  const html = renderToStaticMarkup(createElement(ActionApprovalRow, { threadId: "th", runId: "r", tool }));
  const headline = html.indexOf("Send an email: outside.example");
  const agent = html.indexOf("The agent&#x27;s description: Send the routine weekly digest to the team");
  expect(headline).toBeGreaterThan(-1);
  expect(agent).toBeGreaterThan(headline);
  expect(html).toContain("Goes to an outside domain: outside.example");
  expect(html).toContain("to: audit@outside.example");
  expect(html.slice(0, headline)).not.toContain("weekly digest");
});

it("shows the action kind, not the agent's text, when no facts came", () => {
  const html = renderToStaticMarkup(createElement(ActionApprovalRow, {
    threadId: "th", runId: "r", tool: { ...tool, approval: { ...tool.approval, facts: null } },
  }));
  expect(html).toContain("Resend email");
  expect(html.indexOf("weekly digest")).toBeGreaterThan(html.indexOf("The agent"));
});
