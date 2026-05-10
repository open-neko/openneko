/**
 * /api/briefing GET contract tests. Asserts the v0.9 message sequence
 * and that DB-backed snapshots flow through to the dataModel.
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
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  db,
  metric,
  metric_snapshot,
  pool,
} from "@neko/db";
import type { A2UIMessage } from "@/a2ui/types";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/briefing] skipping: Postgres unreachable.");
}

describeIfDb("/api/briefing GET", () => {
  let orgId: string;
  let GET: typeof import("@/app/api/briefing/route").GET;

  beforeAll(async () => {
    const mod = await import("@/app/api/briefing/route");
    GET = mod.GET;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("api-briefing");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("returns the v0.9 message sequence: createSurface + updateDataModel + updateComponents", async () => {
    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    expect(res.status).toBe(200);
    const messages = res.body as A2UIMessage[];
    expect(Array.isArray(messages)).toBe(true);
    expect(messages.length).toBe(3);

    expect("createSurface" in messages[0]).toBe(true);
    expect("updateDataModel" in messages[1]).toBe(true);
    expect("updateComponents" in messages[2]).toBe(true);

    for (const m of messages) expect(m.version).toBe("v0.9");
  });

  it("falls back to mock ROLE_DATA insights when no DB metrics", async () => {
    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    const messages = res.body as A2UIMessage[];
    const componentsMsg = messages[2] as Extract<A2UIMessage, { updateComponents: unknown }>;
    const root = componentsMsg.updateComponents.components.find((c) => c.id === "root");
    expect(root).toBeDefined();
    // No DB rows → flagged as example (legacy ROLE_DATA mock).
    expect((root as { isExample?: boolean }).isExample).toBe(true);
  });

  it("uses DB snapshots when seeded metrics exist", async () => {
    // Seed a metric + snapshot. The route should return real data and
    // mark the surface as not-example.
    const ins = await db()
      .insert(metric)
      .values({
        org_id: orgId,
        role: "CEO",
        slug: "revenue-mtd",
        source: "bootstrap",
        title: "Revenue this month",
        why: "Quick read on the top line",
        chart_hint: "kpi",
        active: true,
      })
      .returning({ id: metric.id });
    const metricId = ins[0]!.id;
    await db().insert(metric_snapshot).values({
      metric_id: metricId,
      status: "good",
      payload: {
        headlineMetric: "$5.20M",
        headlineLabel: "Revenue MTD",
        insightText: "Up vs last month.",
        detailText: "Driven by enterprise renewals.",
        chartType: "kpi",
        chartData: [{ d: "Revenue MTD", v: 5_200_000, t: 4_900_000 }],
      },
    });

    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    const messages = res.body as A2UIMessage[];
    const componentsMsg = messages[2] as Extract<A2UIMessage, { updateComponents: unknown }>;
    const root = componentsMsg.updateComponents.components.find((c) => c.id === "root");
    expect((root as { isExample?: boolean }).isExample).toBe(false);

    // The DB-backed insight should appear in the data model.
    const dataMsg = messages[1] as Extract<A2UIMessage, { updateDataModel: unknown }>;
    const dataValue = dataMsg.updateDataModel.value as {
      insights: Record<string, { metric: string; mood: string }>;
    };
    expect(dataValue.insights["revenue-mtd"]).toMatchObject({
      metric: "$5.20M",
      mood: "good",
    });
    // OK state for cards with a snapshot.
    expect(dataValue.insights["revenue-mtd"]).toMatchObject({ state: "ok" });
  });

  it("renders state='pending' when a metric has no snapshot yet", async () => {
    await db().insert(metric).values({
      org_id: orgId,
      role: "CEO",
      slug: "still-pending",
      source: "bootstrap",
      title: "Still loading",
      why: "Just kicked off",
      chart_hint: "kpi",
      active: true,
      last_refresh_status: "pending",
    });

    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    const messages = res.body as A2UIMessage[];
    const dataMsg = messages[1] as Extract<A2UIMessage, { updateDataModel: unknown }>;
    const dataValue = dataMsg.updateDataModel.value as {
      insights: Record<string, { state?: string; metric: string }>;
    };
    expect(dataValue.insights["still-pending"].state).toBe("pending");
    expect(dataValue.insights["still-pending"].metric).toBe("Fetching…");
  });

  it("renders state='pending' when a re-run is in flight over an existing snapshot", async () => {
    // A re-run flips last_refresh_status to 'pending' but leaves the
    // old snapshot in place until the new one lands. The API must not
    // keep showing the stale value as state='ok' — the user expects a
    // skeleton until fresh data arrives.
    const ins = await db()
      .insert(metric)
      .values({
        org_id: orgId,
        role: "CEO",
        slug: "rerunning",
        source: "bootstrap",
        title: "Re-running revenue",
        why: "Quick read on the top line",
        chart_hint: "kpi",
        active: true,
        last_refresh_status: "pending",
      })
      .returning({ id: metric.id });
    await db().insert(metric_snapshot).values({
      metric_id: ins[0]!.id,
      status: "good",
      payload: {
        headlineMetric: "$5.20M",
        headlineLabel: "Revenue MTD",
        insightText: "Old number from a previous run.",
        detailText: "Stale.",
        chartType: "kpi",
        chartData: [{ d: "Revenue MTD", v: 5_200_000 }],
      },
    });

    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    const messages = res.body as A2UIMessage[];
    const dataMsg = messages[1] as Extract<A2UIMessage, { updateDataModel: unknown }>;
    const dataValue = dataMsg.updateDataModel.value as {
      insights: Record<string, { state?: string; metric: string; label: string }>;
    };
    expect(dataValue.insights["rerunning"]).toMatchObject({
      state: "pending",
      metric: "Fetching…",
      label: "",
    });
  });

  it("renders state='failed' + error when last refresh failed", async () => {
    await db().insert(metric).values({
      org_id: orgId,
      role: "CEO",
      slug: "broke",
      source: "bootstrap",
      title: "Broken metric",
      why: "Data source died",
      chart_hint: "kpi",
      active: true,
      last_refresh_status: "failed",
      last_refresh_error: "graphjin returned 500",
    });

    const res = await callRoute(GET, { url: "http://localhost/api/briefing?role=CEO" });
    const messages = res.body as A2UIMessage[];
    const dataMsg = messages[1] as Extract<A2UIMessage, { updateDataModel: unknown }>;
    const dataValue = dataMsg.updateDataModel.value as {
      insights: Record<string, { state?: string; error?: string; metric: string }>;
    };
    expect(dataValue.insights["broke"]).toMatchObject({
      state: "failed",
      error: "graphjin returned 500",
      metric: "Couldn't load",
    });
  });
});
