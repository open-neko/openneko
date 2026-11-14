import { expect } from "vitest";
import { body, type Case } from "../src/case";

export const settingsCases: Case[] = [
  { op: "GET /settings/agent", name: "reports the agent settings", status: 200 },
  { op: "PUT /settings/agent", name: "saves the agent settings", status: 200, request: { body: { backend: "ax", globalCap: 3, terminal: true } } },
  { op: "PUT /settings/agent", name: "refuses an unknown backend", status: 400, request: { body: { backend: "nothing" } } },
  { op: "GET /settings/channel-identities", name: "lists the channel identities", status: 200 },
  { op: "PATCH /settings/channel-identities/{id}", name: "refuses an unknown identity", status: 404, request: (c) => ({ params: { id: c.ids.unknownId }, body: { action: "block" } }) },
  { op: "GET /settings/data-source", name: "reports the data source", status: 200 },
  { op: "PUT /settings/data-source", name: "needs a GraphQL URL", status: 400, request: { body: {} } },
  { op: "POST /settings/data-source/test", name: "needs a GraphQL URL", status: 400, request: { body: {} } },
  { op: "GET /settings/data-sources", name: "lists the data sources", status: 200 },
  { op: "PUT /settings/data-sources", name: "refuses a bad name", status: 400, request: { body: { name: "Not Valid" } } },
  { op: "POST /settings/file-sources", name: "needs the file store", status: 503, request: { body: {} } },
  { op: "GET /settings/graphjin-config", name: "reports the setting", status: 200 },
  {
    op: "PATCH /settings/graphjin-config", name: "keeps ask-based config on", status: 200, request: { body: { sourceConfigEnabled: true } },
    check: (r) => expect(body(r).settings).toEqual({ sourceConfigEnabled: true }),
  },
  { op: "PATCH /settings/graphjin-config", name: "refuses a body that is not JSON", status: 400, request: { raw: { body: "{", type: "application/json" } } },
  { op: "GET /settings/install-policy", name: "reports the policy", status: 200 },
  { op: "PATCH /settings/install-policy", name: "keeps Git installs off", status: 200, request: { body: { allowGitUrlInstalls: false } } },
  { op: "PATCH /settings/install-policy", name: "refuses a body that is not JSON", status: 400, request: { raw: { body: "{", type: "application/json" } } },
  { op: "GET /settings/openapi-specs", name: "lists the imported specs", status: 200 },
  { op: "POST /settings/openapi-specs", name: "needs a URL or a file", status: 400, request: { body: {} } },
  { op: "GET /settings/persona", name: "reports the persona", status: 200 },
  {
    op: "PUT /settings/persona", name: "saves the member's own persona", as: "member", status: 200,
    request: { body: { displayName: "Contract Member", roleTemplate: "coo", focusAreas: ["stock"] } },
  },
  {
    op: "PUT /settings/persona", name: "refuses another user's persona to a member", as: "member", status: 403,
    request: (c) => ({ body: { userId: c.ids.adminUserId, displayName: "Not mine" } }),
  },
  { op: "GET /settings/provider", name: "reports the model provider", status: 200 },
  { op: "PUT /settings/provider", name: "refuses an unknown provider", status: 400, request: { body: { scope: "research" } } },
  { op: "POST /settings/provider/test", name: "needs a scope", status: 400, request: { body: {} } },
  { op: "GET /settings/skill-learn", name: "reports the setting", status: 200 },
  { op: "PATCH /settings/skill-learn", name: "keeps skill learning off", status: 200, request: { body: { enabled: false } } },
  { op: "PATCH /settings/skill-learn", name: "needs a boolean", status: 400, request: { body: {} } },
  {
    op: "POST /settings/source-check", name: "reports a source it cannot reach", status: 200,
    request: { body: { type: "postgres", host: "127.0.0.1", port: 9, dbname: "none", user: "none" } },
    check: (r) => expect(body(r).check).toMatchObject({ ok: false }),
  },
  { op: "GET /settings/source-secrets", name: "lists the secret names", status: 200 },
  {
    op: "PUT /settings/source-secrets", name: "stores a secret", status: 200,
    request: { body: { name: "contract.password", value: "planted-contract-secret", description: "Contract test" } },
  },
  {
    op: "GET /settings/source-secrets", name: "lists the name and hides the value", status: 200,
    check: (r) => {
      expect(JSON.stringify(body(r))).toContain("contract.password");
      expect(JSON.stringify(body(r))).not.toContain("planted-contract-secret");
    },
  },
  { op: "PUT /settings/source-secrets", name: "refuses a bad name", status: 400, request: { body: { name: "has space", value: "x" } } },
  { op: "DELETE /settings/source-secrets", name: "removes the secret", status: 200, request: { query: { name: "contract.password" } } },
  { op: "DELETE /settings/source-secrets", name: "needs a name", status: 400 },
  { op: "GET /profile", name: "reports no business profile", status: 404 },
  { op: "PATCH /profile", name: "needs a field", status: 400, request: { body: {} } },
  {
    op: "GET /my/passkeys", name: "lists the signed-in user's passkeys", status: 200,
    check: (r) => expect(body(r)).toEqual({ passkeys: [] }),
  },
  {
    op: "POST /my/passkeys/challenge", name: "issues a registration challenge", status: 200,
    request: { body: { purpose: "register" } },
    check: (r) => expect(body(r)).toMatchObject({ purpose: "register", rp: { name: "OpenNeko" } }),
  },
  {
    op: "POST /my/passkeys/challenge", name: "refuses an unknown purpose", status: 400,
    request: { body: { purpose: "other" } },
  },
  {
    op: "POST /my/passkeys", name: "needs a valid registration payload", status: 400,
    request: { body: {} },
  },
  {
    op: "DELETE /my/passkeys/{passkeyId}", name: "refuses an unknown passkey", status: 404,
    request: (c) => ({ params: { passkeyId: c.ids.unknownId } }),
  },
];
