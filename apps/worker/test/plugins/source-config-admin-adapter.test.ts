// OL5 — the source_config_admin adapter configures the CUSTOMER GraphJin
// (roles, per-source access, source registration) via its admin-only
// gj_config two-phase preview→apply, once an admin approves the proposal.
// These tests pin the three guarantees: correct mutation shape against the
// customer engine, the hard boundary against the OpenNeko internal GraphJin,
// and that connection secrets stay value-blind to the agent/payload/audit.

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { data_source, data_source_secret, db, eq, pool } from "@neko/db";
import {
  createActionRequest,
  executeApprovedActionRequest,
} from "@neko/llm/workflows";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import { inProcessControlPlane } from "@neko/llm/work";
import { registerSourceConfigAdminAdapter } from "../../src/plugins/manage-adapters.js";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[source-config-admin-adapter] skipping: Postgres unreachable.");
}

const CUSTOMER = "http://customer-gj:8080/api/v1/graphql";
const INTERNAL = "http://127.0.0.1:8089";

/** Stub global fetch to emulate the customer GraphJin gj_config/gj_catalog. */
function installFetchMock() {
  const calls: { url: string; body: { query: string } }[] = [];
  const fn = vi.fn(async (url: string, init: { body: string }) => {
    const body = JSON.parse(init.body) as { query: string };
    calls.push({ url, body });
    const q = body.query;
    let payload: unknown;
    if (q.includes('mode: "preview"')) {
      payload = {
        data: {
          gj_config: {
            valid: true,
            preview_id: "pv1",
            change_summary_json: '["changed"]',
            errors_json: "[]",
          },
        },
      };
    } else if (q.includes('mode: "apply"')) {
      payload = {
        data: { gj_config: { applied: true, catalog_revision: "rev2", errors_json: "null" } },
      };
    } else if (q.includes("gj_catalog")) {
      const kind = q.match(/kind: \{ eq: "(\w+)" \}/)?.[1] ?? "database";
      payload = { data: { gj_catalog: [{ id: `${kind}-1`, name: `${kind} one`, summary: "s" }] } };
    } else {
      payload = { data: { gj_config: { catalog_revision: "rev1" } } };
    }
    return { ok: true, json: async () => payload, text: async () => "" } as Response;
  });
  vi.stubGlobal("fetch", fn);
  return { calls };
}

describeIfDb("source_config_admin adapter (OL5)", () => {
  const orgId = uniqueOrgId("srccfg");

  beforeAll(async () => {
    registerSourceConfigAdminAdapter();
    process.env.OPENNEKO_GRAPHJIN_URL = INTERNAL;
    await createTestOrg(orgId);
    await db().insert(data_source).values({
      org_id: orgId,
      kind: "graphjin",
      graphql_url: CUSTOMER,
      name: "default",
      is_default: true,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  async function setEndpoint(url: string) {
    await db()
      .update(data_source)
      .set({ graphql_url: url })
      .where(eq(data_source.org_id, orgId));
  }

  async function runAction(payload: Record<string, unknown>) {
    const request = await createActionRequest({
      orgId,
      scope: "internal",
      kind: "source_config_admin",
      target: String(payload.action ?? ""),
      payload,
      riskLevel: "high",
      status: "approved",
      summary: "test",
    });
    const result = await executeApprovedActionRequest(orgId, request.id);
    return { request, result };
  }

  it("set_source_access runs the two-phase preview→apply with the right shape", async () => {
    await setEndpoint(CUSTOMER);
    const { calls } = installFetchMock();
    const { result } = await runAction({
      action: "set_source_access",
      source: "adventureworks",
      read: "authenticated",
      write: "blocked",
      delete: "blocked",
    });
    expect(result.ok).toBe(true);

    // rev read → preview → apply, all to the customer endpoint.
    expect(calls).toHaveLength(3);
    expect(calls.every((c) => c.url === CUSTOMER)).toBe(true);

    const preview = calls[1].body.query;
    expect(preview).toContain('mode: "preview"');
    expect(preview).toContain('expected_catalog_revision: "rev1"');
    expect(preview).toContain("source_patches");

    const apply = calls[2].body.query;
    expect(apply).toContain('mode: "apply"');
    expect(apply).toContain('preview_id: "pv1"');
    expect(apply).toContain("applied"); // selects `applied`, not `valid`
  });

  it("register_source uses update_sources (upsert), never sources: (replace)", async () => {
    await setEndpoint(CUSTOMER);
    const { calls } = installFetchMock();
    const { result } = await runAction({
      action: "register_source",
      name: "warehouse",
      kind: "database",
      host: "wh-db",
      port: 5432,
      dbname: "warehouse",
      user: "reader",
    });
    expect(result.ok).toBe(true);
    const preview = calls[1].body.query;
    expect(preview).toContain("update_sources");
    // Must not wipe the meta-source via a full replace or a non-existent field.
    expect(preview).not.toContain("source_add");
    expect(preview).not.toMatch(/[^_]sources:/);
  });

  it("BOUNDARY: refuses to configure the OpenNeko internal GraphJin", async () => {
    await setEndpoint(`${INTERNAL}/api/v1/graphql`);
    const { calls } = installFetchMock();
    const { result } = await runAction({
      action: "set_source_access",
      source: "adventureworks",
      read: "authenticated",
    });
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/internal GraphJin/i);
    // Nothing was applied — no request ever reached an engine.
    expect(calls).toHaveLength(0);
  });

  it("SECRET-BLIND: the value reaches the gj_config POST only, never the payload/result", async () => {
    await setEndpoint(CUSTOMER);
    const SECRET_NAME = "WAREHOUSE_DB_PASSWORD";
    const SECRET_VALUE = "s3cr3t-pw-xyz";
    await db()
      .insert(data_source_secret)
      .values({
        org_id: orgId,
        name: SECRET_NAME,
        value_enc: maybeEncryptSecret(SECRET_VALUE),
        updated_at: new Date(),
      })
      .onConflictDoNothing();

    const { calls } = installFetchMock();
    const { request, result } = await runAction({
      action: "register_source",
      name: "warehouse2",
      kind: "database",
      host: "wh-db",
      port: 5432,
      dbname: "warehouse",
      user: "reader",
      secretRef: SECRET_NAME,
    });
    expect(result.ok).toBe(true);

    // The resolved value IS injected into the apply POST body (worker-side).
    const applyBody = calls.find((c) => c.body.query.includes('mode: "apply"'))!;
    expect(applyBody.body.query).toContain(SECRET_VALUE);

    // …but it is NOWHERE in the action payload (what the agent proposed)…
    expect(JSON.stringify(request.payload)).not.toContain(SECRET_VALUE);
    expect(JSON.stringify(request.payload)).toContain(SECRET_NAME);
    // …nor in the adapter result (which carries the NAME only).
    expect(JSON.stringify(result)).not.toContain(SECRET_VALUE);
    expect(JSON.stringify(result)).toContain(SECRET_NAME);
  });

  it("describeSourceGraph summarizes gj_catalog rows for the customer engine", async () => {
    await setEndpoint(CUSTOMER);
    installFetchMock();
    const graph = await inProcessControlPlane.describeSourceGraph({ orgId });
    expect(graph.reachable).toBe(true);
    expect(graph.host).toBe("customer-gj");
    expect(graph.databases?.[0]?.id).toBe("database-1");
    expect(graph.capabilities?.[0]?.id).toBe("capability-1");
    expect(graph.namespaces?.[0]?.id).toBe("namespace-1");
  });
});
