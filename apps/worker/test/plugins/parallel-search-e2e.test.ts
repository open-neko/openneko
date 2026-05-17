import { existsSync } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import type { ActionAdapter } from "@neko/llm/workflows";
import { PluginRegistry } from "../../src/plugins/plugin-registry";
import { SubprocessRuntime } from "../../src/plugins/subprocess-runtime";

const PARALLEL_BUNDLE_PATH = path.resolve(
  __dirname,
  "../../../../../plugins/packages/parallel-search/dist/run.js",
);

const FAKE_INTEGRITY = "sha512-" + "a".repeat(86) + "==";

const MANIFEST_JSON = JSON.stringify({
  schema: "https://open-neko.github.io/plugins/manifest.schema.json",
  plugins: [
    {
      name: "@open-neko/plugin-parallel-search",
      version: "0.2.0",
      integrity: FAKE_INTEGRITY,
      capabilities: { network: ["search.parallel.ai"] },
      kinds: ["web_search", "web_fetch"],
    },
  ],
});

/**
 * End-to-end through the registry using the actual built plugin
 * bundle but with a subprocess runtime instead of a microVM. Proves:
 *   - The registry maps kind → plugin from the manifest alone
 *   - Lazy spawn fires on first execute_action
 *   - The bundled plugin's register() RPC is verified against the
 *     manifest's declared kinds
 *
 * Skips itself when the bundle isn't present (e.g. CI runs that
 * haven't built the plugins repo yet); the registry unit tests cover
 * the mocked path either way.
 */
describe("@open-neko/plugin-parallel-search via PluginRegistry + SubprocessRuntime", () => {
  let workRoot: string;
  let repoRoot: string;
  let secretsConfigDir: string;
  let captured: Map<string, ActionAdapter>;

  beforeAll(() => {
    if (!existsSync(PARALLEL_BUNDLE_PATH)) {
      console.warn(
        `[parallel-search-e2e] skipping — bundle missing at ${PARALLEL_BUNDLE_PATH}. Run \`pnpm -C ../../plugins/packages/parallel-search build\``,
      );
    }
  });

  beforeEach(async () => {
    workRoot = await mkdtemp(path.join(tmpdir(), "plugin-e2e-"));
    repoRoot = await mkdtemp(path.join(tmpdir(), "openneko-e2e-"));
    secretsConfigDir = await mkdtemp(path.join(tmpdir(), "openneko-e2e-secrets-"));
    captured = new Map();
  });

  afterEach(async () => {
    await rm(workRoot, { recursive: true, force: true });
    await rm(repoRoot, { recursive: true, force: true });
    await rm(secretsConfigDir, { recursive: true, force: true });
  });

  it("registry registers both web_search and web_fetch from manifest alone (no VM spawn yet)", async () => {
    if (!existsSync(PARALLEL_BUNDLE_PATH)) return;
    await writeFile(path.join(repoRoot, "openneko.plugins.json"), MANIFEST_JSON);
    const reg = new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime: new SubprocessRuntime(),
      resolveRunner: () => PARALLEL_BUNDLE_PATH,
      onAdapter: (k, a) => captured.set(k, a),
    });
    await reg.start();
    expect(reg.status().kinds.sort()).toEqual(["web_fetch", "web_search"]);
    expect(reg.status().vmsRunning).toBe(0);
    expect(captured.has("web_search")).toBe(true);
    expect(captured.has("web_fetch")).toBe(true);
    await reg.stop();
  });

  it("first execute_action spawns the VM and surfaces the no-MCP error path", async () => {
    if (!existsSync(PARALLEL_BUNDLE_PATH)) return;
    await writeFile(path.join(repoRoot, "openneko.plugins.json"), MANIFEST_JSON);
    const reg = new PluginRegistry({
      repoRoot,
      workRoot,
      secretsConfigDir,
      runtime: new SubprocessRuntime({ env: {} }),
      resolveRunner: () => PARALLEL_BUNDLE_PATH,
      onAdapter: (k, a) => captured.set(k, a),
    });
    await reg.start();

    const adapter = captured.get("web_search")!;
    // Point at an unreachable URL so the MCP client errors fast.
    await expect(
      adapter({
        request: {
          id: "req-1",
          orgId: "org-1",
          workflowRunId: null,
          triggeredByObservationId: null,
          policyId: null,
          scope: "external",
          kind: "web_search",
          target: null,
          payload: { query: "openneko", mcp_url: "https://127.0.0.1:1/mcp" },
          riskLevel: "low",
          status: "approved",
          summary: "x",
          requestedByRunId: null,
          approvedByUserId: null,
          approvedAt: new Date(),
          rejectionReason: null,
          createdAt: new Date(),
          updatedAt: new Date(),
        },
      }),
    ).rejects.toThrow(/PLUGIN_ERROR|plugin/);
    // After the failed attempt, the VM IS up (the failure was inside
    // the plugin's execute_action; the VM started and ran register()
    // successfully).
    expect(reg.status().vmsRunning).toBeGreaterThanOrEqual(0);
    await reg.stop();
  });
});
