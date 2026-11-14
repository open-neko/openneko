import { expect } from "vitest";
import { body, type Case } from "../src/case";

const workflowParams = (c: { ids: Record<string, string> }) => ({ workflowId: c.ids.adminWorkflowId! });

export const workflowCases: Case[] = [
  {
    op: "GET /workflows", name: "lists every workflow for an admin", status: 200,
    check: (r, c) => expect((body(r).workflows as { id: string }[]).map((w) => w.id)).toEqual(expect.arrayContaining([c.ids.adminWorkflowId, c.ids.memberWorkflowId])),
  },
  {
    op: "GET /workflows", name: "lists only the member's own workflow", as: "member", status: 200,
    check: (r, c) => expect((body(r).workflows as { id: string }[]).map((w) => w.id)).toEqual([c.ids.memberWorkflowId]),
  },
  { op: "GET /workflows/{workflowId}", name: "reads a workflow", status: 200, request: (c) => ({ params: workflowParams(c) }) },
  { op: "GET /workflows/{workflowId}", name: "hides another owner's workflow from a member", as: "member", status: 404, request: (c) => ({ params: workflowParams(c) }) },
  { op: "GET /workflows/{workflowId}", name: "refuses an unknown workflow", status: 404, request: (c) => ({ params: { workflowId: c.ids.unknownId } }) },
  {
    op: "PATCH /workflows/{workflowId}", name: "turns the schedule off", status: 200,
    request: (c) => ({ params: workflowParams(c), body: { cronEnabled: false } }),
  },
  { op: "PATCH /workflows/{workflowId}", name: "needs a known field", status: 400, request: (c) => ({ params: workflowParams(c), body: {} }) },
  { op: "POST /workflows/{workflowId}/adopt", name: "refuses an unknown personal workflow", status: 404, request: (c) => ({ params: { workflowId: c.ids.unknownId } }) },
  { op: "GET /workflows/{workflowId}/api-access", name: "reports API access", status: 200, request: (c) => ({ params: workflowParams(c) }) },
  {
    op: "POST /workflows/{workflowId}/api-access", name: "turns API access on and gives a key once", status: 201,
    request: (c) => ({ params: workflowParams(c), body: { action: "enable" } }),
    check: (r, c) => {
      c.ids.workflowApiKey = String(body(r).token);
    },
  },
  { op: "POST /workflows/{workflowId}/api-access", name: "refuses an unknown action", status: 400, request: (c) => ({ params: workflowParams(c), body: { action: "nothing" } }) },
  {
    op: "PATCH /workflows/{workflowId}/api-access", name: "changes a limit", status: 200,
    request: (c) => ({ params: workflowParams(c), body: { includeTelemetry: false } }),
  },
  { op: "PATCH /workflows/{workflowId}/api-access", name: "needs a limit", status: 400, request: (c) => ({ params: workflowParams(c), body: {} }) },
  {
    op: "POST /workflows/{workflowId}/runs", name: "refuses a run without an idempotency key", as: "anonymous", status: 400,
    request: (c) => ({ params: workflowParams(c), headers: { authorization: `Bearer ${c.ids.workflowApiKey}` }, body: {} }),
  },
  {
    op: "POST /workflows/{workflowId}/runs", name: "refuses a wrong key", as: "anonymous", status: 401,
    request: (c) => ({ params: workflowParams(c), headers: { authorization: "Bearer wrong", "idempotency-key": "contract-0001" }, body: {} }),
  },
  {
    op: "GET /workflows/{workflowId}/runs/{runId}", name: "refuses an unknown run", as: "anonymous", status: 404,
    request: (c) => ({ params: { ...workflowParams(c), runId: c.ids.unknownId }, headers: { authorization: `Bearer ${c.ids.workflowApiKey}` } }),
  },
  {
    op: "GET /workflows/{workflowId}/runs/{runId}/artifact", name: "refuses an unknown run", as: "anonymous", status: 404,
    request: (c) => ({ params: { ...workflowParams(c), runId: c.ids.unknownId }, headers: { authorization: `Bearer ${c.ids.workflowApiKey}` } }),
  },
  { op: "DELETE /workflows/{workflowId}/api-access", name: "turns API access off", status: 200, request: (c) => ({ params: workflowParams(c) }) },
  {
    op: "POST /workflows/{workflowId}/manual-runs", name: "refuses an unknown workflow", status: 400,
    request: (c) => ({ params: { workflowId: c.ids.unknownId }, body: {} }),
  },
  { op: "GET /workflows/{workflowId}/subscriptions", name: "lists the subscriptions", status: 200, request: (c) => ({ params: workflowParams(c) }) },
  {
    op: "POST /workflows/{workflowId}/subscriptions", name: "subscribes to watch findings", status: 200,
    request: (c) => ({ params: workflowParams(c), body: { sourceKind: "workflow_output", filter: { mood: "watch" } } }),
    check: (r, c) => {
      c.ids.subscriptionId = String((body(r).subscription as { id: string }).id);
    },
  },
  { op: "POST /workflows/{workflowId}/subscriptions", name: "needs a source kind", status: 400, request: (c) => ({ params: workflowParams(c), body: {} }) },
  { op: "PATCH /subscriptions/{subscriptionId}", name: "pauses the subscription", status: 200, request: (c) => ({ params: { subscriptionId: c.ids.subscriptionId }, body: { enabled: false } }) },
  { op: "PATCH /subscriptions/{subscriptionId}", name: "refuses an unknown subscription", status: 404, request: (c) => ({ params: { subscriptionId: c.ids.unknownId }, body: { enabled: false } }) },
  { op: "DELETE /subscriptions/{subscriptionId}", name: "deletes the subscription", status: 200, request: (c) => ({ params: { subscriptionId: c.ids.subscriptionId } }) },
  { op: "DELETE /subscriptions/{subscriptionId}", name: "refuses a deleted subscription", status: 404, request: (c) => ({ params: { subscriptionId: c.ids.subscriptionId } }) },
  {
    op: "POST /workflows/{workflowId}/subscriptions/dry-run", name: "needs a source_change filter", status: 400,
    request: (c) => ({ params: workflowParams(c), body: {} }),
  },
  {
    op: "POST /workflows/{workflowId}/subscriptions/dry-run", name: "refuses an unknown workflow", status: 404,
    request: (c) => ({ params: { workflowId: c.ids.unknownId }, body: { sourceKind: "source_change", filter: { table: "orders", primary_key: ["id"] } } }),
  },
  {
    op: "POST /workflows/{workflowId}/subscriptions/dry-run", name: "hides another owner's workflow from a member", as: "member", status: 404,
    request: (c) => ({ params: workflowParams(c), body: { sourceKind: "source_change", filter: { table: "orders", primary_key: ["id"] } } }),
  },
  {
    op: "POST /workflows/{workflowId}/subscriptions/dry-run", name: "needs a data source", status: 422,
    request: (c) => ({ params: workflowParams(c), body: { sourceKind: "source_change", filter: { table: "orders", primary_key: ["id"] } } }),
  },
  {
    op: "GET /workflow-runs", name: "lists the runs a member can see", as: "member", status: 200,
    check: (r, c) => expect((body(r).runs as { id: string }[]).map((x) => x.id)).toEqual([c.ids.memberWorkflowRunId]),
  },
  { op: "GET /workflow-runs/{workflowRunId}", name: "reads a run", status: 200, request: (c) => ({ params: { workflowRunId: c.ids.adminWorkflowRunId } }), check: (r) => expect(body(r).canStop).toBe(false) },
  { op: "GET /workflow-runs/{workflowRunId}", name: "hides another owner's run from a member", as: "member", status: 404, request: (c) => ({ params: { workflowRunId: c.ids.adminWorkflowRunId } }) },
  { op: "GET /workflow-runs/{workflowRunId}/artifact", name: "reports a run with no artifact", status: 404, request: (c) => ({ params: { workflowRunId: c.ids.adminWorkflowRunId } }) },
  { op: "POST /hooks/{workflowId}", name: "refuses a workflow without a webhook", as: "anonymous", status: 404, request: (c) => ({ params: workflowParams(c), body: {} }) },
  {
    op: "GET /hooks/{workflowId}/result/{runId}", name: "refuses an unknown run", as: "anonymous", status: 404,
    request: (c) => ({ params: { ...workflowParams(c), runId: c.ids.unknownId } }),
  },
  {
    op: "GET /action-requests", name: "lists the member's action requests", as: "member", status: 200,
    check: (r, c) => expect((body(r).actionRequests as { id: string }[]).map((a) => a.id)).toEqual([c.ids.memberActionRequestId]),
  },
  { op: "GET /action-requests/{actionRequestId}", name: "reads an action request", status: 200, request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId } }) },
  { op: "GET /action-requests/{actionRequestId}", name: "hides another owner's request from a member", as: "member", status: 404, request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId } }) },
  {
    op: "GET /approvals", name: "counts only the member's pending approvals", as: "member", status: 200,
    request: { query: { countOnly: "true" } },
    check: (r) => expect(body(r)).toEqual({ count: 1 }),
  },
  {
    op: "PATCH /action-requests/{actionRequestId}", name: "hides another owner's request from a member's decision", as: "member", status: 404,
    request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId }, body: { decision: "approve" } }),
  },
  {
    op: "PATCH /action-requests/{actionRequestId}", name: "refuses an unknown decision", status: 400,
    request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId }, body: { decision: "maybe" } }),
  },
  {
    op: "PATCH /action-requests/{actionRequestId}", name: "rejects the admin's request", status: 200,
    request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId }, body: { decision: "reject", reason: "Contract test" } }),
    check: (r) => expect(body(r).actionRequest).toMatchObject({ status: "rejected" }),
  },
  {
    op: "PATCH /action-requests/{actionRequestId}", name: "refuses a second decision", status: 409,
    request: (c) => ({ params: { actionRequestId: c.ids.adminActionRequestId }, body: { decision: "approve" } }),
  },
  {
    op: "GET /approvals", name: "lists the member's pending approvals", as: "member", status: 200,
    check: (r, c) => expect((body(r).actions as { id: string }[]).map((a) => a.id)).toEqual([c.ids.memberActionRequestId]),
  },
  { op: "GET /observations", name: "lists the observations", status: 200 },
  { op: "POST /insights/ensure", name: "queues the insights job", status: 200, request: { body: {} } },
  { op: "DELETE /workflows/{workflowId}", name: "refuses an unknown workflow", status: 404, request: (c) => ({ params: { workflowId: c.ids.unknownId } }) },
  { op: "DELETE /workflows/{workflowId}", name: "hides another owner's workflow from a member", as: "member", status: 404, request: (c) => ({ params: workflowParams(c) }) },
];

export const finalCases: Case[] = [
  { op: "DELETE /workflows/{workflowId}", name: "deletes the member's own workflow", as: "member", status: 200, request: (c) => ({ params: { workflowId: c.ids.memberWorkflowId } }) },
  { op: "GET /workflows/{workflowId}", name: "refuses a deleted workflow", as: "member", status: 404, request: (c) => ({ params: { workflowId: c.ids.memberWorkflowId } }) },
];
