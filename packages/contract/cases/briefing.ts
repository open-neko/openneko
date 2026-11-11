import { expect } from "vitest";
import { body, type Case } from "../src/case";

export const briefingCases: Case[] = [
  {
    op: "GET /briefing/stats", name: "counts only what the member can open", as: "member", status: 200,
    check: (r) => expect(body(r)).toEqual({ runsToday: 1, findingsToday: 1, pendingApprovals: 1, budgetPct: 10 }),
  },
  {
    op: "GET /briefing/stats", name: "counts every workflow for an admin", status: 200,
    check: (r) => expect(body(r)).toEqual({ runsToday: 2, findingsToday: 2, pendingApprovals: 2, budgetPct: 10 }),
  },
  { op: "GET /briefing", name: "renders the briefing surface", status: 200 },
  { op: "POST /briefing", name: "needs a question", status: 400, request: { body: {} } },
  { op: "GET /briefing/by-metric", name: "refuses an unknown metric", status: 404, request: (c) => ({ query: { metricId: c.ids.unknownId! } }) },
  { op: "GET /briefing/by-metric", name: "needs a metric", status: 400 },
  { op: "POST /briefing/cards", name: "refuses an unknown observation", status: 404, request: (c) => ({ body: { observationId: c.ids.unknownId } }) },
  { op: "PATCH /briefing/cards", name: "refuses an unknown card", status: 404, request: (c) => ({ body: { cardId: c.ids.unknownId } }) },
  {
    op: "GET /briefing/findings", name: "shows the member only their own findings", as: "member", status: 200,
    check: (r, c) => expect(JSON.stringify(body(r))).not.toContain(c.ids.adminOutputId),
  },
  { op: "POST /briefing/mute", name: "mutes a scope for an hour", status: 200, request: { body: { scope: "metric:contract", duration: "1h" } } },
  { op: "POST /briefing/mute", name: "needs a scope", status: 400, request: { body: {} } },
  {
    op: "GET /briefing/mute", name: "lists the mute", status: 200,
    check: (r) => expect(JSON.stringify(body(r))).toContain("metric:contract"),
  },
  { op: "DELETE /briefing/mute", name: "unmutes the scope", status: 200, request: { query: { scope: "metric:contract" } } },
  { op: "DELETE /briefing/mute", name: "needs a scope", status: 400 },
  { op: "PATCH /briefing/pin", name: "needs a metric and a flag", status: 400, request: { body: {} } },
  {
    op: "POST /briefing/pins", name: "pins the member's finding", as: "member", status: 200,
    request: (c) => ({ body: { outputId: c.ids.memberOutputId } }),
    check: (r, c) => {
      c.ids.pinId = String((body(r).pin as { id: string }).id);
    },
  },
  { op: "POST /briefing/pins", name: "needs an output", status: 400, request: { body: {} } },
  { op: "GET /briefing/pins", name: "lists the pin", as: "member", status: 200 },
  { op: "DELETE /briefing/pins/{pinId}", name: "removes the pin", as: "member", status: 200, request: (c) => ({ params: { pinId: c.ids.pinId } }) },
  { op: "DELETE /briefing/pins/{pinId}", name: "refuses a removed pin", as: "member", status: 404, request: (c) => ({ params: { pinId: c.ids.pinId } }) },
  { op: "GET /briefing/recent-actions", name: "lists recent actions", status: 200 },
  { op: "POST /briefing/retry", name: "refuses an unknown metric", status: 404, request: (c) => ({ body: { metricId: c.ids.unknownId } }) },
  { op: "GET /briefing/status", name: "refuses an unknown job", status: 404, request: (c) => ({ query: { jobId: c.ids.unknownId } }) },
  { op: "GET /briefing/value", name: "reports the time saved", status: 200 },
];
