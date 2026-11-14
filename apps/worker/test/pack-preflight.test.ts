import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SolutionPackBundle } from "@neko/packs";

const { graphjinQuery, enqueueMetricRefresh } = vi.hoisted(() => ({ graphjinQuery: vi.fn(), enqueueMetricRefresh: vi.fn() }));

vi.mock("@neko/llm/graphjin", () => ({
  graphjinQuery,
  mintGraphjinToken: () => "fixture-token",
}));

vi.mock("@neko/db/jobs", () => ({ enqueueMetricRefresh }));

import { PackPreflightError, enqueuePackMetricRefreshes, metricsRefreshOn, runPackReadPreflight } from "../src/packs/preflight.js";

function pack(): SolutionPackBundle {
  return {
    manifest: {
      metadata: { id: "third-party-pack" },
      health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} },
    },
    artifacts: [{
      kind: "saved_query",
      key: "query.account_profile",
      targetRef: "account_profile",
      path: "graphjin/saved-queries/account_profile.gql",
      hash: "fixture",
      content: "query AccountProfile { customer_api_get_profile(accountId: \"current\") { displayName } }",
    }],
  } as SolutionPackBundle;
}

describe("pack query preflight errors", () => {
  beforeEach(() => graphjinQuery.mockReset());

  it("identifies the pack artifact and preserves GraphJin error details", async () => {
    graphjinQuery.mockResolvedValue({
      data: null,
      errors: [{ message: "table not found: customer_api_get_profile", path: ["customer_api_get_profile"] }],
    });

    const failure = await runPackReadPreflight(pack(), "http://graphjin.test", "org-test", {})
      .then(() => null, (error: unknown) => error);

    expect(failure).toBeInstanceOf(PackPreflightError);
    expect(failure).toMatchObject({
      diagnostic: {
        code: "pack_query_preflight_failed",
        phase: "query_preflight",
        artifact: {
          kind: "saved_query",
          name: "account_profile",
          path: "graphjin/saved-queries/account_profile.gql",
        },
        causes: ["table not found: customer_api_get_profile (path: customer_api_get_profile)"],
      },
    });
    expect((failure as Error).message).toContain("graphjin/saved-queries/account_profile.gql");
    expect((failure as Error).message).toContain("table not found: customer_api_get_profile");
  });
});

describe("pack metric refreshes", () => {
  it("are off unless OPENNEKO_METRICS_REFRESH is on", () => {
    expect(metricsRefreshOn({})).toBe(false);
    expect(metricsRefreshOn({ OPENNEKO_METRICS_REFRESH: "off" })).toBe(false);
    expect(metricsRefreshOn({ OPENNEKO_METRICS_REFRESH: " ON " })).toBe(true);
  });

  it("queue nothing on install with the default setting", async () => {
    vi.stubEnv("OPENNEKO_METRICS_REFRESH", "");
    const bundle = { ...pack(), artifacts: [{ kind: "metric", key: "metric.revenue", targetRef: "revenue", path: "metrics/revenue.yaml", hash: "fixture", content: "role: CEO\n" }] } as unknown as SolutionPackBundle;
    await expect(enqueuePackMetricRefreshes("org", bundle)).resolves.toBe(0);
    expect(enqueueMetricRefresh).not.toHaveBeenCalled();
    vi.unstubAllEnvs();
  });
});
