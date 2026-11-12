import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import { AGENT_BACKEND_OPTIONS, AGENT_DEFAULT_GLOBAL_CAP } from "@neko/llm";
import {
  getAgentSettings,
  getAgentSettingsPayload,
} from "@/lib/agent-backend-settings";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[agent-settings] skipping: metadata Postgres unreachable.");
}

describeIfDb("agent-settings", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("agent-settings");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    await clearProvider(orgId, "primary");
  });

  it("returns Ax concurrency defaults with no row", async () => {
    await expect(getAgentSettings(orgId)).resolves.toEqual({
      source: "default",
      backend: "ax",
      globalCap: AGENT_DEFAULT_GLOBAL_CAP,
      terminal: true,
    });
  });

  it("returns the backend and its only option", async () => {
    await expect(getAgentSettingsPayload(orgId)).resolves.toEqual({
      agent: {
        source: "default",
        backend: "ax",
        globalCap: AGENT_DEFAULT_GLOBAL_CAP,
        terminal: true,
      },
      options: AGENT_BACKEND_OPTIONS,
      terminalAvailable: false,
      defaults: { globalCap: AGENT_DEFAULT_GLOBAL_CAP, terminal: true },
    });
    expect(AGENT_BACKEND_OPTIONS.map((o) => o.value)).toEqual(["ax"]);
  });

});
