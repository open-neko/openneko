import { expect } from "vitest";
import { body, type Case } from "../src/case";

const id = (r: { body: unknown }, key: string) => String((body(r as never)[key] as { id: string }).id);

export const adminCases: Case[] = [
  {
    op: "GET /admin/services", name: "reports each external service", status: 200, request: { query: { refresh: "1" } },
    check: (r) => {
      const services = body(r).services as { id: string; state: string }[];
      expect(services.map((s) => s.id)).toEqual(["inference", "embedding", "sandbox", "file-store"]);
    },
  },
  { op: "GET /admin/services", name: "is for administrators only", as: "member", status: 403 },
  {
    op: "GET /admin/overview", name: "reports the counts for the index pages", status: 200,
    check: (r) => {
      expect(typeof body(r).setupComplete).toBe("boolean");
    },
  },
  { op: "GET /admin/overview", name: "is for administrators only", as: "member", status: 403 },
  { op: "GET /admin/change-password", name: "reports the password state", status: 200 },
  {
    op: "POST /admin/change-password", name: "refuses an install that connects by URL", status: 409,
    request: { body: { password: "contract-password-0123" } },
  },
  { op: "GET /admin/channels/settings", name: "needs the worker", status: 502 },
  {
    op: "POST /admin/channels/settings", name: "needs the worker", status: 502,
    request: { body: { channel: "slack", values: {} } },
  },
  { op: "GET /admin/data-access", name: "lists the rules", status: 200 },
  { op: "GET /admin/data-access/settings", name: "reports the setting", status: 200 },
  {
    op: "PATCH /admin/data-access/settings", name: "needs the worker", status: 503,
    request: { body: { enabled: false } },
  },
  { op: "PATCH /admin/data-access/settings", name: "needs a boolean", status: 400, request: { body: {} } },
  { op: "GET /admin/data-access/tables", name: "needs a source", status: 400 },
  {
    op: "POST /admin/data-access", name: "needs every field", status: 400,
    request: { body: { groupId: "x" } },
  },
  { op: "DELETE /admin/data-access/{ruleId}", name: "refuses an unknown rule", status: 404, request: (c) => ({ params: { ruleId: c.ids.unknownId } }) },
  {
    op: "GET /admin/devices", name: "lists both seeded devices", status: 200,
    check: (r, c) => {
      const devices = body(r).devices as { id: string; userId: string; name: string }[];
      expect(devices.map((d) => d.userId).sort()).toEqual(["contract-admin", "contract-member"]);
      c.ids.memberDeviceId = devices.find((d) => d.userId === "contract-member")!.id;
    },
  },
  { op: "DELETE /admin/devices/{deviceId}", name: "refuses an unknown device", status: 404, request: (c) => ({ params: { deviceId: c.ids.unknownId } }) },
  { op: "GET /admin/directory", name: "needs the worker", status: 503 },
  { op: "POST /admin/directory", name: "needs the worker", status: 503, request: { body: {} } },
  {
    op: "GET /admin/groups", name: "lists the built-in groups", status: 200,
    check: (r) => expect((body(r).groups as { slug: string }[]).map((g) => g.slug)).toEqual(expect.arrayContaining(["administrators", "everyone"])),
  },
  {
    op: "POST /admin/groups", name: "creates a group", status: 201,
    request: { body: { name: "Contract buyers", description: "Made by the contract tests" } },
    check: (r, c) => {
      c.ids.groupId = id(r, "group");
    },
  },
  { op: "POST /admin/groups", name: "refuses a group with no name", status: 400, request: { body: {} } },
  { op: "GET /admin/groups/{groupId}", name: "reads the new group", status: 200, request: (c) => ({ params: { groupId: c.ids.groupId } }) },
  { op: "GET /admin/groups/{groupId}", name: "refuses an unknown group", status: 404, request: (c) => ({ params: { groupId: c.ids.unknownId } }) },
  {
    op: "PATCH /admin/groups/{groupId}", name: "renames the group", status: 200,
    request: (c) => ({ params: { groupId: c.ids.groupId }, body: { name: "Contract planners" } }),
    check: (r) => expect(body(r).group).toMatchObject({ name: "Contract planners" }),
  },
  {
    op: "POST /admin/groups/{groupId}/members", name: "adds the member", status: 200,
    request: (c) => ({ params: { groupId: c.ids.groupId }, body: { userId: c.ids.memberUserId } }),
  },
  {
    op: "GET /admin/users/{userId}/access", name: "shows the member's new group", status: 200,
    request: (c) => ({ params: { userId: c.ids.memberUserId } }),
    check: (r) => expect(body(r).administrator).toBe(false),
  },
  { op: "GET /admin/items", name: "lists the item types", status: 200 },
  {
    op: "POST /admin/item-grants", name: "grants the member's workflow to the group", status: 200,
    request: (c) => ({ body: { groupId: c.ids.groupId, itemType: "workflow", itemId: c.ids.memberWorkflowId } }),
  },
  {
    op: "GET /admin/item-grants", name: "lists the holders of the workflow", status: 200,
    request: (c) => ({ query: { itemType: "workflow", itemId: c.ids.memberWorkflowId } }),
  },
  { op: "GET /admin/item-grants", name: "needs an item", status: 400 },
  {
    op: "DELETE /admin/item-grants", name: "removes the grant", status: 200,
    request: (c) => ({ body: { groupId: c.ids.groupId, itemType: "workflow", itemId: c.ids.memberWorkflowId } }),
  },
  { op: "POST /admin/item-grants", name: "needs a group and an item", status: 400, request: { body: {} } },
  {
    op: "DELETE /admin/groups/{groupId}/members", name: "removes the member", status: 200,
    request: (c) => ({ params: { groupId: c.ids.groupId }, body: { userId: c.ids.memberUserId } }),
  },
  { op: "GET /admin/idp-rules", name: "lists the rules", status: 200 },
  { op: "POST /admin/idp-rules", name: "needs both groups", status: 400, request: { body: {} } },
  {
    op: "POST /admin/idp-rules", name: "refuses an unknown directory group", status: 404,
    request: (c) => ({ body: { ssoGroupId: c.ids.unknownId, userGroupId: c.ids.groupId } }),
  },
  { op: "DELETE /admin/idp-rules/{ruleId}", name: "removes nothing for an unknown rule", status: 200, request: (c) => ({ params: { ruleId: c.ids.unknownId } }) },
  { op: "DELETE /admin/groups/{groupId}", name: "deletes the group", status: 200, request: (c) => ({ params: { groupId: c.ids.groupId } }) },
  { op: "DELETE /admin/groups/{groupId}", name: "refuses a deleted group", status: 404, request: (c) => ({ params: { groupId: c.ids.groupId } }) },
  { op: "GET /admin/packs", name: "needs the worker", status: 502 },
  { op: "GET /admin/packs/magento/store-management", name: "needs the worker", status: 502 },
  { op: "POST /admin/packs/magento/store-management", name: "needs the worker", status: 502, request: { body: {} } },
  { op: "POST /admin/packs/upload", name: "needs a ZIP archive", status: 415, request: { raw: { body: "not a zip", type: "text/plain" } } },
  { op: "GET /admin/packs/{packId}/{action}", name: "refuses an unknown operation", status: 404, request: { params: { packId: "magento", action: "nothing" } } },
  { op: "POST /admin/packs/{packId}/{action}", name: "refuses an unknown operation", status: 404, request: { params: { packId: "magento", action: "nothing" }, body: {} } },
  { op: "GET /admin/public-url", name: "reports the address", status: 200 },
  {
    op: "PUT /admin/public-url", name: "saves an address", status: 200,
    request: { body: { url: "https://openneko.contract.test" } },
    check: (r) => expect(body(r).saved).toBe("https://openneko.contract.test"),
  },
  { op: "PUT /admin/public-url", name: "clears the address", status: 200, request: { body: { url: "" } } },
  { op: "PUT /admin/public-url", name: "refuses an address that is not a URL", status: 400, request: { body: { url: "not a url" } } },
  { op: "GET /admin/reckon-webhooks", name: "lists the webhooks", status: 200 },
  {
    op: "POST /admin/reckon-webhooks", name: "adds a webhook for a workflow", status: 201,
    request: (c) => ({ body: { reckonWorkflowId: "contract-hook", workflowId: c.ids.adminWorkflowId, token: "contract-hook-token-0123456789" } }),
  },
  { op: "POST /admin/reckon-webhooks", name: "needs every field", status: 400, request: { body: {} } },
  { op: "DELETE /admin/reckon-webhooks/{reckonWorkflowId}", name: "removes the webhook", status: 200, request: { params: { reckonWorkflowId: "contract-hook" } } },
  { op: "DELETE /admin/reckon-webhooks/{reckonWorkflowId}", name: "refuses a removed webhook", status: 404, request: { params: { reckonWorkflowId: "contract-hook" } } },
  { op: "GET /admin/signin/status", name: "reports the provider and the users", status: 200 },
  { op: "POST /admin/signin/provider", name: "refuses an unknown provider", status: 400, request: { body: { provider: "nothing" } } },
  { op: "POST /admin/signin/email-provider", name: "refuses an unknown provider", status: 400, request: { body: { provider: "nothing", from: "a@b.test" } } },
  { op: "POST /admin/signin/test", name: "needs an email address", status: 400, request: { body: { email: "nope" } } },
  { op: "GET /admin/models", name: "lists the models and routes", status: 200 },
  {
    op: "POST /admin/models", name: "adds a model and shows its key only as a mask", status: 201,
    request: {
      body: {
        name: "contract-cheap", provider: "openai-compatible", model: "qwen3",
        config: { url: "https://models.contract.test/v1" }, secrets: { apiKey: "sk-contract-planted-0193" },
        price: { inputUsdPer1M: 0.5, outputUsdPer1M: 2 },
      },
    },
    check: (r, c) => {
      c.ids.modelId = id(r, "model");
      expect(JSON.stringify(r.body)).not.toContain("sk-contract-planted-0193");
    },
  },
  {
    op: "POST /admin/models", name: "refuses a name in use", status: 409,
    request: { body: { name: "contract-cheap", provider: "openai-compatible", model: "q", config: { url: "https://models.contract.test/v1" } } },
  },
  { op: "POST /admin/models", name: "refuses a provider not in the list", status: 400, request: { body: { name: "x", provider: "no-such" } } },
  { op: "PUT /admin/models/{modelId}", name: "changes the model", status: 200, request: (c) => ({ params: { modelId: c.ids.modelId }, body: { model: "qwen3-large" } }) },
  { op: "PUT /admin/models/{modelId}", name: "refuses an unknown model", status: 404, request: (c) => ({ params: { modelId: c.ids.unknownId }, body: {} }) },
  { op: "PUT /admin/model-routes", name: "routes agent turns to the model", status: 200, request: (c) => ({ body: { task: "turn", modelId: c.ids.modelId } }) },
  { op: "PUT /admin/model-routes", name: "keeps the default route on the default model", status: 400, request: (c) => ({ body: { task: "default", modelId: c.ids.modelId } }) },
  { op: "PUT /admin/model-routes", name: "refuses an unknown model", status: 404, request: (c) => ({ body: { task: "classify", modelId: c.ids.unknownId } }) },
  { op: "DELETE /admin/models/{modelId}", name: "refuses a model a route uses", status: 409, request: (c) => ({ params: { modelId: c.ids.modelId } }) },
  { op: "PUT /admin/model-routes", name: "clears the turn route", status: 200, request: { body: { task: "turn" } } },
  { op: "DELETE /admin/models/{modelId}", name: "removes the model", status: 200, request: (c) => ({ params: { modelId: c.ids.modelId } }) },
  { op: "DELETE /admin/models/{modelId}", name: "refuses an unknown model", status: 404, request: (c) => ({ params: { modelId: c.ids.unknownId } }) },
  { op: "GET /admin/spend", name: "reports the limits", status: 200 },
  {
    op: "PUT /admin/spend", name: "saves the limits", status: 200,
    request: { body: { runCapUsd: 5, orgHourlyUsd: 200, orgDailyUsd: 500, workflowHourlyUsd: 50, workflowDailyUsd: 200, warnPercent: 80 } },
  },
  { op: "PUT /admin/spend", name: "refuses a negative cap", status: 400, request: { body: { runCapUsd: -1 } } },
  { op: "DELETE /admin/spend/alerts/{alertId}", name: "refuses an unknown alert", status: 404, request: (c) => ({ params: { alertId: c.ids.unknownId } }) },
  {
    op: "PUT /admin/spend/workflows/{workflowId}", name: "sets a workflow ceiling", status: 200,
    request: (c) => ({ params: { workflowId: c.ids.adminWorkflowId }, body: { hourlyUsd: 10, dailyUsd: 40 } }),
  },
  {
    op: "DELETE /admin/spend/workflows/{workflowId}", name: "removes the workflow ceiling", status: 200,
    request: (c) => ({ params: { workflowId: c.ids.adminWorkflowId } }),
  },
  {
    op: "PUT /admin/workflow-api-limits", name: "saves the limits", status: 200,
    request: { body: { rollingTokenBudget: 1_000_000, rollingCostMicrosBudget: 50_000_000 } },
  },
  { op: "PUT /admin/workflow-api-limits", name: "refuses a budget out of range", status: 400, request: { body: { rollingTokenBudget: 1, rollingCostMicrosBudget: 1 } } },
  { op: "GET /admin/workflow-api-limits", name: "reports the limits", status: 200 },
  {
    op: "GET /admin/users", name: "lists the users", status: 200,
    check: (r) => {
      const users = body(r).users as { id: string; role: string }[];
      expect(users.map((u) => u.id)).toEqual(expect.arrayContaining(["contract-admin", "contract-member"]));
    },
  },
  { op: "GET /admin/users", name: "is for administrators only", as: "member", status: 403 },
  {
    op: "POST /admin/users", name: "adds a member", status: 201,
    request: { body: { email: "new.member@contract.test", name: "New Member", role: "member" } },
    check: (r, c) => {
      c.ids.newUserId = id(r, "user");
    },
  },
  { op: "POST /admin/users", name: "needs an email address", status: 400, request: { body: { email: "nope", role: "member" } } },
  {
    op: "PATCH /admin/users/{userId}", name: "turns the new member off", status: 200,
    request: (c) => ({ params: { userId: c.ids.newUserId }, body: { disabled: true } }),
  },
  { op: "PATCH /admin/users/{userId}", name: "needs a change", status: 400, request: (c) => ({ params: { userId: c.ids.newUserId }, body: {} }) },
  { op: "GET /onboarding/status", name: "reports the onboarding state", status: 200 },
  { op: "POST /onboarding/submit", name: "needs a company name", status: 400, request: { body: {} } },
  { op: "GET /policies", name: "lists the policies", status: 200 },
  { op: "GET /policies/{policyId}", name: "refuses an unknown policy", status: 404, request: (c) => ({ params: { policyId: c.ids.unknownId } }) },
  { op: "PATCH /policies/{policyId}", name: "needs a change", status: 400, request: (c) => ({ params: { policyId: c.ids.unknownId }, body: {} }) },
];
