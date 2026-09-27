import { execFile } from "node:child_process";
import { promisify } from "node:util";
import pg from "pg";
import { expect, it, vi } from "vitest";
import type { SubscriptionRecord } from "../src/workflows/store";

const runCommand = promisify(execFile);
let active: SubscriptionRecord[] = [];
vi.mock("../src/workflows/store", async importOriginal => ({
  ...await importOriginal<typeof import("../src/workflows/store")>(),
  listEnabledSubscriptions: async () => active,
}));

import { startSubscriptionManager } from "../src/workflows/subscription-manager";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("reconnects a real GraphJin source-change websocket after restart", async () => {
  if (process.env.RECORDS_PG_PORT !== "18120") throw Error("isolated GraphJin fixture required");
  const database = new pg.Client({host: "127.0.0.1", port: 18120,
    user: "fixture", password: "fixture", database: "fixture"});
  await database.connect();
  const errors: string[] = [];
  const labels: string[] = [];
  const subscription: SubscriptionRecord = {
    id: "harness-ws-source", orgId: "harness-ws-org", workflowId: "harness-ws-workflow",
    sourceKind: "source_change", enabled: true,
    filter: {table: "references", primary_key: ["id"], select: ["label"]},
    debounceMs: 0, maxConcurrentRuns: 1, maxChainDepthOverride: null,
    idempotencyKeyTemplate: null, createdAt: new Date(), updatedAt: new Date(),
  };
  active = [subscription];
  const manager = startSubscriptionManager({
    resolveTransport: async () => ({baseUrl: "http://127.0.0.1:18117/api/v1/graphql"}),
    refreshIntervalMs: 60_000,
    onMatch: event => {
      if (event.kind === "source_change") labels.push(String(event.match.snapshot.label));
    },
    onError: error => { errors.push(error.message); },
  });
  const waitFor = async (label: string) => {
    for (let attempt = 0; attempt < 120; attempt++) {
      if (labels.includes(label)) return;
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    throw Error(`GraphJin websocket did not deliver ${label}: ${errors.join("; ")}`);
  };
  try {
    await database.query("update \"references\" set label='REF-WS-1' where id=42");
    await manager.ready;
    await waitFor("REF-WS-1");
    await runCommand("docker", ["restart", "harness-m3-graphjin-1"], {timeout: 60_000});
    await database.query("update \"references\" set label='REF-WS-2' where id=42");
    await waitFor("REF-WS-2");
    expect(manager.activeSubscriptionIds()).toEqual([subscription.id]);
    console.log("M5_GRAPHJIN_WEBSOCKET_RECONNECT_PASS", labels.length);
  } finally {
    await manager.stop();
    active = [];
    await database.query("update \"references\" set label='REF-42' where id=42");
    await database.end();
  }
}, 90_000);
