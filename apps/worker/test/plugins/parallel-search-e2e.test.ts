import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import type { PluginManifest } from "@open-neko/plugin-types";
import { loadPlugins } from "../../src/plugins/load-plugins";
import { SubprocessRuntime } from "../../src/plugins/subprocess-runtime";

const PARALLEL_BUNDLE_PATH = path.resolve(
  __dirname,
  "../../../../../plugins/packages/parallel-search/dist/run.js",
);

const SAMPLE_MANIFEST: PluginManifest = {
  schema: "https://open-neko.github.io/plugins/manifest.schema.json",
  plugins: [
    {
      name: "@open-neko/plugin-parallel-search",
      version: "0.2.0",
      integrity: "sha512-" + "a".repeat(86) + "==",
      capabilities: { network: ["search.parallel.ai"] },
    },
  ],
};

/**
 * End-to-end through the loader using the actual built plugin bundle —
 * but with a subprocess runtime instead of a microVM. Proves:
 *   - The loader copies the runner correctly
 *   - The plugin's register() RPC parses on the worker side
 *   - The adapter wires up by kind and proxies execute_action through
 *     the runtime back into the plugin
 *
 * Skips itself when the bundle isn't present (e.g. CI runs that
 * haven't built the plugins repo yet); the unit tests cover the
 * mocked path either way.
 */
describe("@open-neko/plugin-parallel-search via SubprocessRuntime", () => {
  let workRoot: string;
  let repoRoot: string;

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
  });

  afterEach(async () => {
    await rm(workRoot, { recursive: true, force: true });
    await rm(repoRoot, { recursive: true, force: true });
  });

  it("register() over a real bundle returns both web_search and web_fetch", async () => {
    if (!existsSync(PARALLEL_BUNDLE_PATH)) return;
    const runtime = new SubprocessRuntime();
    const handle = await loadPlugins({
      repoRoot,
      workRoot,
      manifest: SAMPLE_MANIFEST,
      runtime,
      resolveRunner: () => PARALLEL_BUNDLE_PATH,
    });
    expect(handle.result.loaded).toEqual([
      {
        name: "@open-neko/plugin-parallel-search",
        version: "0.2.0",
        actionKinds: ["web_search", "web_fetch"],
      },
    ]);
    expect(handle.result.skipped).toEqual([]);
    await handle.shutdown();
  });

  it("execute_action against an unreachable MCP URL fails with a plugin error", async () => {
    if (!existsSync(PARALLEL_BUNDLE_PATH)) return;
    const runtime = new SubprocessRuntime({ env: {} });
    await loadPlugins({
      repoRoot,
      workRoot,
      manifest: SAMPLE_MANIFEST,
      runtime,
      resolveRunner: () => PARALLEL_BUNDLE_PATH,
    });
    const pluginId = "open-neko-plugin-parallel-search";
    // Point at an unreachable URL so the MCP client errors fast rather
    // than reaching the live Parallel API in the test suite. The
    // assertion is that the worker correctly propagates the plugin's
    // RPC error response through the loader and back.
    const response = await runtime.callRpc(
      pluginId,
      "execute_action",
      JSON.stringify({
        request: {
          id: "req-1",
          orgId: "org-1",
          scope: "external",
          kind: "web_search",
          target: null,
          summary: "x",
          payload: {
            query: "openneko",
            mcp_url: "https://127.0.0.1:1/mcp",
          },
          riskLevel: "low",
        },
      }),
    );
    expect(response.ok).toBe(false);
    if (response.ok) return;
    expect(response.error.code).toBe("PLUGIN_ERROR");
  });
});
