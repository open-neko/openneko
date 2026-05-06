/**
 * Standalone Hermes debug trace for the metric-agent revenue-by-channel
 * card. Seeds a throwaway org, runs ONCE with debug=true (Hermes stderr
 * piped to our stderr), dumps the final MetricAgentResult, drops the org.
 *
 *   ANTHROPIC_API_KEY=... pnpm exec tsx scripts/_debug-hermes.ts
 *
 * Use this when you want to see what the agent actually queried and how
 * its reasoning maps to the final headlineMetric.
 */

import { readFileSync } from "node:fs";
import { provisionHostConfig, runMetricAgent } from "@neko/llm";
import {
  createTestOrg,
  deleteTestOrg,
  seedDataSource,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { db, eq, organization } from "@neko/db";
import { maybeEncryptSecret } from "@neko/llm/secrets";

function readKey(): string | null {
  const fromEnv = process.env.ANTHROPIC_API_KEY?.trim();
  if (fromEnv) return fromEnv;
  // Local debug fallback: read from a file the operator wrote.
  try {
    const stored = readFileSync("/tmp/_neko_anthropic_key", "utf8").trim();
    if (stored) return stored;
  } catch {
    // file missing — fall through
  }
  return null;
}
const ANTHROPIC_KEY = readKey();
if (!ANTHROPIC_KEY) {
  console.error(
    "ANTHROPIC_API_KEY not set (env or /tmp/_neko_anthropic_key file)",
  );
  process.exit(2);
}

const GRAPHQL_URL =
  process.env.NEKO_E2E_GRAPHQL_URL?.trim() || "http://localhost:8080/api/v1/graphql";
const MCP_URL = GRAPHQL_URL.replace(/\/api\/v1\/graphql\/?$/, "/api/v1/mcp");

const orgId = uniqueOrgId("hermes-debug");
console.log(`[debug] seeding throwaway org ${orgId}`);

await createTestOrg(orgId, "Hermes Debug Org");
await db()
  .update(organization)
  .set({ setup_complete_at: new Date() })
  .where(eq(organization.id, orgId));

await seedDataSource(orgId, {
  graphqlUrl: GRAPHQL_URL,
  mcpUrl: MCP_URL,
  label: "primary",
});

await seedProvider(orgId, {
  scope: "primary",
  provider: "anthropic",
  model: "claude-opus-4-7",
  enabled: true,
  secrets: { apiKey: maybeEncryptSecret(ANTHROPIC_KEY) },
});

await seedProvider(orgId, {
  scope: "agent",
  provider: "hermes",
  enabled: true,
  config: { backend: "hermes" },
});

console.log(`[debug] provisioning host config for ${orgId}`);
await provisionHostConfig(orgId);

console.log(`[debug] running metric agent (revenue-by-channel) with debug=true`);
console.log(`[debug] Hermes stderr will stream to this process's stderr`);
console.log("─".repeat(80));

const start = Date.now();
let result;
try {
  result = await runMetricAgent({
    orgId,
    role: "CEO",
    slug: "revenue-by-channel",
    title: "Revenue by sales channel",
    why: "Quick read on where revenue is coming from",
    chartHint: "donut",
    debug: true,
  });
} catch (e) {
  console.error("\n[debug] runMetricAgent threw:", e);
  await deleteTestOrg(orgId);
  process.exit(1);
}
const elapsedMs = Date.now() - start;

console.log("─".repeat(80));
console.log(`[debug] done in ${(elapsedMs / 1000).toFixed(1)}s`);
console.log("");
console.log("=== reasoning ===");
console.log(result.reasoning);
console.log("");
console.log("=== headline ===");
console.log(`headlineMetric: ${result.headlineMetric}`);
console.log(`headlineLabel:  ${result.headlineLabel}`);
console.log(`mood:           ${result.mood}`);
console.log(`chartType:      ${result.chartType}`);
console.log("");
console.log("=== insightText ===");
console.log(result.insightText);
console.log("");
console.log("=== detailText ===");
console.log(result.detailText);
console.log("");
console.log("=== chartData ===");
console.log(JSON.stringify(result.chartData, null, 2));
console.log("");
console.log("=== timeWindow ===");
console.log(JSON.stringify(result.timeWindow, null, 2));
console.log("");
console.log("=== ground truth ===");
console.log("Reseller (B2B):    $80,487,704.18  (3,806 orders, onlineorderflag=false)");
console.log("Online (Internet): $29,358,677.22  (27,659 orders, onlineorderflag=true)");
console.log("Total:             $109,846,381.40 (31,465 orders)");

await deleteTestOrg(orgId);
console.log(`\n[debug] dropped org ${orgId}`);
process.exit(0);
