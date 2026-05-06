/**
 * /api/settings/agent contract tests. Covers payload shape, backend save,
 * and the auto-coerce-primary-to-anthropic side effect when claude-agent
 * is selected.
 */

import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { and, db, eq, llm_provider_config, pool } from "@neko/db";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId, mockProvisionHostConfig } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
  mockProvisionHostConfig: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

vi.mock("@neko/llm", async () => {
  const actual = await vi.importActual<typeof import("@neko/llm")>("@neko/llm");
  return { ...actual, provisionHostConfig: mockProvisionHostConfig };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/settings/agent] skipping: Postgres unreachable.");
}

async function readPrimaryProvider(orgId: string): Promise<string | null> {
  const rows = await db()
    .select({ provider: llm_provider_config.provider })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, "primary"),
      ),
    );
  return rows[0]?.provider ?? null;
}

describeIfDb("/api/settings/agent", () => {
  let orgId: string;
  let GET: typeof import("@/app/api/settings/agent/route").GET;
  let PUT: typeof import("@/app/api/settings/agent/route").PUT;

  beforeAll(async () => {
    const mod = await import("@/app/api/settings/agent/route");
    GET = mod.GET;
    PUT = mod.PUT;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("api-agent");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
    mockProvisionHostConfig.mockResolvedValue(undefined);
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("GET returns the default payload (hermes, 20, 8) when no row", async () => {
    const res = await callRoute(GET);
    expect(res.status).toBe(200);
    const body = res.body as {
      agent: { backend: string; globalCap: number; claudeAgentCap: number; source: string };
      options: unknown[];
    };
    expect(body.agent).toMatchObject({
      source: "default",
      backend: "hermes",
      globalCap: 20,
      claudeAgentCap: 8,
    });
    expect(body.options.length).toBeGreaterThanOrEqual(2);
  });

  it("PUT { backend: 'hermes' } persists and calls provisionHostConfig", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { backend: "hermes", globalCap: 30, claudeAgentCap: 5 },
    });
    expect(res.status).toBe(200);
    const body = res.body as { backend: string; globalCap: number };
    expect(body.backend).toBe("hermes");
    expect(body.globalCap).toBe(30);
    expect(mockProvisionHostConfig).toHaveBeenCalledWith(orgId);
  });

  it("PUT { backend: 'claude-agent' } auto-coerces primary provider to anthropic", async () => {
    await seedProvider(orgId, {
      scope: "primary",
      provider: "google-gemini",
      model: "gemini-pro-latest",
    });
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { backend: "claude-agent" },
    });
    expect(res.status).toBe(200);
    expect(await readPrimaryProvider(orgId)).toBe("anthropic");
  });

  it("PUT rejects unknown backend with 400", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { backend: "openai-agents" },
    });
    expect(res.status).toBe(400);
    expect((res.body as { error: string }).error).toMatch(/Unsupported agent backend/);
    expect(mockProvisionHostConfig).not.toHaveBeenCalled();
  });

  it("PUT preserves existing anthropic primary when switching to claude-agent", async () => {
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: { apiKey: "sk-ant-existing" },
    });
    await callRoute(PUT, { method: "PUT", body: { backend: "claude-agent" } });
    const rows = await db()
      .select({ provider: llm_provider_config.provider, secrets: llm_provider_config.secrets })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, "primary"),
        ),
      );
    expect(rows[0].provider).toBe("anthropic");
    // Existing secret preserved
    const stored = rows[0].secrets as Record<string, unknown>;
    expect(stored.apiKey).toBeTruthy();
  });

  it("clearing the agent row falls back to defaults", async () => {
    await callRoute(PUT, { method: "PUT", body: { backend: "claude-agent" } });
    await clearProvider(orgId, "agent");
    const res = await callRoute(GET);
    const body = res.body as { agent: { source: string; backend: string } };
    expect(body.agent.source).toBe("default");
    expect(body.agent.backend).toBe("hermes");
  });
});
