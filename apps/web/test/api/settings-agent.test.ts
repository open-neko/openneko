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
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { and, db, eq, llm_provider_config, pool } from "@neko/db";
import { AGENT_BACKEND_OPTIONS, AGENT_DEFAULT_GLOBAL_CAP } from "@neko/llm";
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

if (!reachable) console.warn("[api/settings/agent] skipping: Postgres unreachable.");

describeIfDb("/api/settings/agent", () => {
  let orgId: string;
  let GET: typeof import("@/app/api/settings/agent/route").GET;
  let PUT: typeof import("@/app/api/settings/agent/route").PUT;

  beforeAll(async () => {
    ({ GET, PUT } = await import("@/app/api/settings/agent/route"));
  });

  beforeEach(async () => {
    mockGetOrgId.mockClear();
    mockProvisionHostConfig.mockClear();
    orgId = uniqueOrgId("api-agent");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
    mockProvisionHostConfig.mockResolvedValue(undefined);
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
  });

  afterAll(async () => {
    await pool().end();
  });

  it("GET exposes the backend choice and concurrency", async () => {
    const res = await callRoute(GET);
    expect(res.status).toBe(200);
    expect(res.body).toEqual({
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
  });

  it("GET shows the terminal as available when the install has a sandbox URL", async () => {
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "unix:///run/openneko/sandbox.sock");
    try {
      const res = await callRoute(GET);
      expect(res.body).toMatchObject({ terminalAvailable: true, agent: { terminal: true } });
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("PUT turns the terminal off, keeps it off on a later save, and refuses a non-boolean", async () => {
    const off = await callRoute(PUT, { method: "PUT", body: { terminal: false } });
    expect(off.status).toBe(200);
    expect(off.body).toMatchObject({ source: "org", terminal: false });
    const later = await callRoute(PUT, { method: "PUT", body: { globalCap: 4 } });
    expect(later.body).toMatchObject({ globalCap: 4, terminal: false });
    const bad = await callRoute(PUT, { method: "PUT", body: { terminal: "off" } });
    expect(bad.status).toBe(400);
    expect(bad.body).toEqual({ error: "terminal must be true or false" });
  });

  it("PUT rejects a removed backend", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { backend: "removed-runtime", globalCap: 30 },
    });
    expect(res.status).toBe(400);
    expect(res.body).toEqual({ error: "Unsupported agent backend: removed-runtime" });
    expect(mockProvisionHostConfig).not.toHaveBeenCalled();
  });

  it("PUT accepts Ax", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { backend: "ax", globalCap: 12 },
    });
    expect(res.status).toBe(200);
    expect(res.body).toEqual({
      source: "org",
      backend: "ax",
      globalCap: 12,
      terminal: true,
    });
  });

  it("PUT persists the backend and globalCap", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { globalCap: 30 },
    });
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ source: "org", backend: "ax", globalCap: 30, terminal: true });
    expect(mockProvisionHostConfig).toHaveBeenCalledWith(orgId);

    const [row] = await db()
      .select({ provider: llm_provider_config.provider, config: llm_provider_config.config })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, "agent"),
        ),
      );
    expect(row).toEqual({ provider: "ax", config: { backend: "ax", globalCap: 30, terminal: true } });
  });

  it("PUT falls back for an invalid cap", async () => {
    const res = await callRoute(PUT, {
      method: "PUT",
      body: { globalCap: "invalid" },
    });
    expect(res.status).toBe(200);
    expect(res.body).toEqual({
      source: "org",
      backend: "ax",
      globalCap: AGENT_DEFAULT_GLOBAL_CAP,
      terminal: true,
    });
  });
});
