import type { Case } from "../src/case";

const pack = { packId: "contract", connectionKey: "contract" };

export const connectionCases: Case[] = [
  { op: "GET /bots", name: "lists the bots", status: 200 },
  {
    op: "POST /bots", name: "adds a bot", status: 201, request: { body: { name: "Contract cat", coat: "snow", pattern: "tabby" } },
    check: (r, c) => {
      c.ids.botId = String(((r.body as Record<string, unknown>).bot as { id: string }).id);
    },
  },
  { op: "POST /bots", name: "needs a name, a coat and a pattern", status: 400, request: { body: {} } },
  { op: "GET /bots/{botId}", name: "reads the bot", status: 200, request: (c) => ({ params: { botId: c.ids.botId } }) },
  { op: "PATCH /bots/{botId}", name: "renames the bot", status: 200, request: (c) => ({ params: { botId: c.ids.botId }, body: { name: "Contract kitten" } }) },
  { op: "PATCH /bots/{botId}", name: "needs a change", status: 400, request: (c) => ({ params: { botId: c.ids.botId }, body: {} }) },
  {
    op: "GET /bots/{botId}/routines/{workflowId}", name: "refuses a workflow that is not the bot's", status: 404,
    request: (c) => ({ params: { botId: c.ids.botId, workflowId: c.ids.adminWorkflowId } }),
  },
  {
    op: "PATCH /bots/{botId}/routines/{workflowId}", name: "needs a boolean", status: 400,
    request: (c) => ({ params: { botId: c.ids.botId, workflowId: c.ids.adminWorkflowId }, body: {} }),
  },
  { op: "GET /bots/thread/{threadId}", name: "reports a thread with no bot", status: 200, request: (c) => ({ params: { threadId: c.ids.adminThreadId } }) },
  { op: "POST /bots/thread/{threadId}/seen", name: "refuses an unknown thread", status: 404, request: (c) => ({ params: { threadId: c.ids.unknownId } }) },
  { op: "DELETE /bots/{botId}", name: "removes the bot", status: 200, request: (c) => ({ params: { botId: c.ids.botId } }) },
  { op: "DELETE /bots/{botId}", name: "refuses a removed bot", status: 404, request: (c) => ({ params: { botId: c.ids.botId } }) },
  { op: "GET /integrations/list", name: "lists the integrations", status: 200 },
  { op: "GET /integrations/connect/{plugin}/start", name: "refuses an unknown plugin", status: 404, request: { params: { plugin: "contract" } } },
  { op: "GET /integrations/connect/{plugin}/callback", name: "sends a callback without state back", status: 302, request: { params: { plugin: "contract" } } },
  { op: "POST /integrations/disconnect/{plugin}", name: "needs the worker", status: 502, request: { params: { plugin: "contract" } } },
  { op: "GET /library", name: "lists the documents", status: 200 },
  { op: "GET /library/documents/{id}", name: "refuses an unknown document", status: 404, request: (c) => ({ params: { id: c.ids.unknownId } }) },
  { op: "DELETE /library/documents/{id}", name: "refuses an unknown document", status: 404, request: (c) => ({ params: { id: c.ids.unknownId } }) },
  { op: "POST /library/upload", name: "needs a multipart body", status: 400, request: { body: {} } },
  {
    op: "POST /my/pack-accounts/{packId}/{connectionKey}", name: "refuses another site", status: 403,
    request: { params: pack, headers: { origin: "https://elsewhere.test" }, body: {} },
  },
  {
    op: "DELETE /my/pack-accounts/{packId}/{connectionKey}", name: "refuses another site", status: 403,
    request: { params: pack, headers: { origin: "https://elsewhere.test" } },
  },
  { op: "GET /pack-accounts/{packId}/{connectionKey}", name: "needs the worker", status: 502, request: { params: pack } },
  { op: "POST /pack-accounts/{packId}/{connectionKey}", name: "needs a client id", status: 400, request: { params: pack, body: {} } },
  { op: "DELETE /pack-accounts/{packId}/{connectionKey}", name: "needs the worker", status: 502, request: { params: pack } },
  { op: "POST /pack-accounts/{packId}/{connectionKey}/start", name: "needs a client id", status: 400, request: { params: pack, body: {} } },
  { op: "GET /pack-accounts/{packId}/{connectionKey}/callback", name: "refuses a callback without state", status: 400, request: { params: pack } },
  { op: "GET /sso/setup/status", name: "refuses an anonymous caller once sign-in is on", as: "anonymous", status: 403 },
  { op: "GET /sso/setup/status", name: "needs the worker", status: 500 },
  { op: "POST /sso/setup/check", name: "needs the worker", status: 500 },
  { op: "POST /sso/setup/credentials", name: "needs an environment URL", status: 400, request: { body: {} } },
  { op: "GET /sso/setup/credentials-info", name: "needs an environment", status: 400 },
  { op: "GET /sso/setup/environments", name: "needs the worker", status: 500 },
  { op: "POST /sso/setup/generate-portal-link", name: "needs the worker", status: 500 },
  { op: "POST /sso/setup/ids", name: "needs an environment", status: 400, request: { body: {} } },
  { op: "GET /sso/setup/organizations", name: "needs an environment", status: 400 },
];
