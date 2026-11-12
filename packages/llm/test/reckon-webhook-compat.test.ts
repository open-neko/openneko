import { describe, expect, it } from "vitest";
import { pool } from "@neko/db";
import {
  importCompatWebhook,
  RECKON_SEED_KEY,
  reckonSeedMessageFrom,
  removeCompatWebhook,
  sha256Hex,
} from "../src/workflows/compat";

async function dbReachable(): Promise<boolean> {
  try {
    await pool().query("select 1 from compat_webhook limit 1");
    return true;
  } catch {
    return false;
  }
}

const describeIfDb = (await dbReachable()) ? describe : describe.skip;
const TOKEN = "reckon-token-0123456789abcdef";

describe("reckon webhook seed", () => {
  it("reads the start message the Go front stores", () => {
    expect(reckonSeedMessageFrom({ [RECKON_SEED_KEY]: { mode: "batch", message: "Begin." } })).toBe("Begin.");
    expect(reckonSeedMessageFrom({ [RECKON_SEED_KEY]: { message: "  " } })).toBeNull();
    expect(reckonSeedMessageFrom({ other: 1 })).toBeNull();
    expect(reckonSeedMessageFrom(null)).toBeNull();
  });
});

describeIfDb("reckon webhook import", () => {
  it("imports a webhook, hides the token, and enables API access with compat limits", async () => {
    const orgId = `reckon-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
    const reckonId = "01JIMPORT00000000000000000";
    await pool().query("insert into organization (id, name) values ($1, 'Reckon compat test')", [orgId]);
    try {
      const { rows } = await pool().query<{ id: string }>(
        "insert into workflow_definition (org_id, name) values ($1, 'Daily lead union') returning id",
        [orgId],
      );
      const workflowId = rows[0]!.id;
      await importCompatWebhook({ orgId, actorUserId: null, reckonWorkflowId: reckonId, workflowId, token: TOKEN, params: ["date"] });
      const webhook = await pool().query(
        "select org_id, workflow_id, params, batch_chunk_size, token_sha256 from compat_webhook where reckon_workflow_id = $1",
        [reckonId],
      );
      expect(webhook.rows[0]).toEqual({
        org_id: orgId,
        workflow_id: workflowId,
        params: ["date"],
        batch_chunk_size: 1_000,
        token_sha256: sha256Hex(TOKEN),
      });
      const access = await pool().query(
        "select enabled, max_tokens_per_run, max_runtime_seconds, max_tool_calls from workflow_api_access where workflow_id = $1",
        [workflowId],
      );
      expect(access.rows[0]).toEqual({
        enabled: true,
        max_tokens_per_run: 3_000_000,
        max_runtime_seconds: 1_800,
        max_tool_calls: 1_024,
      });
      expect(await removeCompatWebhook({ orgId, actorUserId: null, reckonWorkflowId: reckonId })).toBe(true);
      const left = await pool().query("select 1 from compat_webhook where reckon_workflow_id = $1", [reckonId]);
      expect(left.rows).toEqual([]);
    } finally {
      await pool().query("delete from organization where id = $1", [orgId]);
    }
  });
});
