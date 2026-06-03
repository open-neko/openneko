import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ActionAdapter } from "@neko/llm/workflows";
import { PluginRegistry } from "../../src/plugins/plugin-registry";
import { OpenShellRuntime } from "../../src/plugins/openshell-runtime";

/**
 * FULL product path on OpenShell, against a real (host) gateway: the real
 * PluginRegistry lazily spawns an OpenShell sandbox on first action, uploads
 * the real @open-neko/plugin-parallel-search bundle, runs the version-checked
 * register(), then dispatches execute_action. Proves the plugin runtime swap
 * works end-to-end — not just the runtime in isolation.
 *
 * Opt-in (creates Docker sandboxes via the gateway): a reachable OpenShell
 * gateway (brew service or compose) + the base image built + the bundle:
 *   docker build -f docker/plugin-base.Dockerfile -t ghcr.io/open-neko/plugin-base:node20 .
 *   pnpm -C ../../plugins/packages/parallel-search build
 *   OPENNEKO_OPENSHELL_E2E=1 pnpm --filter @neko/worker test openshell-registry-e2e
 */
const PARALLEL_BUNDLE_PATH = path.resolve(
  __dirname,
  "../../../../../plugins/packages/parallel-search/dist/run.js",
);
const BASE_IMAGE =
  process.env.OPENNEKO_PLUGIN_BASE_IMAGE ?? "ghcr.io/open-neko/plugin-base:node20";
const FAKE_INTEGRITY = "sha512-" + "a".repeat(86) + "==";

const MANIFEST_JSON = JSON.stringify({
  schema: "https://open-neko.github.io/plugins/manifest.schema.json",
  plugins: [
    {
      name: "@open-neko/plugin-parallel-search",
      version: "0.2.0",
      integrity: FAKE_INTEGRITY,
      permissions: { network: ["search.parallel.ai"], env: [] },
      capabilities: {
        action: {
          kinds: [
            { kind: "web_search", description: "Search the web" },
            { kind: "web_fetch", description: "Fetch a page" },
          ],
        },
      },
    },
  ],
});

function cmdOk(cmd: string, args: string[]): boolean {
  try {
    return spawnSync(cmd, args, { stdio: "ignore" }).status === 0;
  } catch {
    return false;
  }
}

const CAN_RUN =
  process.env.OPENNEKO_OPENSHELL_E2E === "1" &&
  existsSync(PARALLEL_BUNDLE_PATH) &&
  cmdOk("openshell", ["--version"]) &&
  cmdOk("openshell", ["sandbox", "list"]) &&
  cmdOk("docker", ["image", "inspect", BASE_IMAGE]);

describe.skipIf(!CAN_RUN)(
  "PluginRegistry + OpenShellRuntime (real gateway, real plugin)",
  () => {
    let workRoot: string;
    let repoRoot: string;
    let secretsConfigDir: string;
    let captured: Map<string, ActionAdapter>;
    let reg: PluginRegistry | undefined;

    beforeEach(async () => {
      workRoot = await mkdtemp(path.join(tmpdir(), "oss-reg-e2e-"));
      repoRoot = await mkdtemp(path.join(tmpdir(), "oss-reg-e2e-repo-"));
      secretsConfigDir = await mkdtemp(path.join(tmpdir(), "oss-reg-e2e-sec-"));
      captured = new Map();
    });

    afterEach(async () => {
      await reg?.stop().catch(() => {});
      reg = undefined;
      await rm(workRoot, { recursive: true, force: true });
      await rm(repoRoot, { recursive: true, force: true });
      await rm(secretsConfigDir, { recursive: true, force: true });
    });

    it("registers kinds from the manifest without spawning a sandbox", async () => {
      await writeFile(path.join(repoRoot, "openneko.plugins.json"), MANIFEST_JSON);
      reg = new PluginRegistry({
        repoRoot,
        workRoot,
        secretsConfigDir,
        runtime: new OpenShellRuntime({ image: BASE_IMAGE, bundleDir: workRoot, onLog: () => {} }),
        resolveRunner: () => PARALLEL_BUNDLE_PATH,
        onAdapter: (k, a) => captured.set(k, a),
      });
      await reg.start();
      expect(reg.status().kinds.sort()).toEqual(["web_fetch", "web_search"]);
      expect(reg.status().vmsRunning).toBe(0);
    });

    it(
      "first execute_action spawns the sandbox, version-checks register(), and dispatches",
      async () => {
        await writeFile(path.join(repoRoot, "openneko.plugins.json"), MANIFEST_JSON);
        reg = new PluginRegistry({
          repoRoot,
          workRoot,
          secretsConfigDir,
          runtime: new OpenShellRuntime({ image: BASE_IMAGE, bundleDir: workRoot, onLog: () => {} }),
          resolveRunner: () => PARALLEL_BUNDLE_PATH,
          onAdapter: (k, a) => captured.set(k, a),
        });
        await reg.start();

        const adapter = captured.get("web_search")!;
        // Unreachable MCP URL (and not in the sandbox's egress allowlist), so
        // the action runs inside the sandbox and fails fast — proving the full
        // spawn → register(version-check) → execute_action path executed.
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
        ).rejects.toThrow();

        // The sandbox is up: register() succeeded (version 0.2.0 matched) and
        // the plugin booted in OpenShell before execute_action ran.
        expect(reg.status().vmsRunning).toBe(1);
      },
      240_000,
    );
  },
);
