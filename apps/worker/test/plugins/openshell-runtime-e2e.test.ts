import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import { OpenShellRuntime } from "../../src/plugins/openshell-runtime";

/**
 * Real-OpenShell loop: build the lean base, boot a sandbox, upload the
 * actual @open-neko/plugin-slack bundle, and run `register` through a real
 * `OpenShellRuntime` against the live gateway. Proves create/upload/exec/
 * delete + per-host policy work end-to-end — the mocked unit tests cover
 * the argv, this covers the gateway.
 *
 * Opt-in (creates Docker containers): set OPENNEKO_OPENSHELL_E2E=1 with a
 * running gateway and the base image built:
 *   docker build -f docker/plugin-base.Dockerfile -t ghcr.io/open-neko/plugin-base:node20 .
 *   pnpm -C ../../plugins/packages/slack build
 *   OPENNEKO_OPENSHELL_E2E=1 pnpm --filter @neko/worker test openshell-runtime-e2e
 */
const SLACK_BUNDLE = path.resolve(
  __dirname,
  "../../../../../plugins/packages/slack/dist/run.js",
);
const BASE_IMAGE =
  process.env.OPENNEKO_PLUGIN_BASE_IMAGE ?? "ghcr.io/open-neko/plugin-base:node20";

function cmdOk(cmd: string, args: string[]): boolean {
  try {
    return spawnSync(cmd, args, { stdio: "ignore" }).status === 0;
  } catch {
    return false;
  }
}

const CAN_RUN =
  process.env.OPENNEKO_OPENSHELL_E2E === "1" &&
  existsSync(SLACK_BUNDLE) &&
  cmdOk("openshell", ["--version"]) &&
  cmdOk("openshell", ["status"]) &&
  cmdOk("docker", ["image", "inspect", BASE_IMAGE]);

describe.skipIf(!CAN_RUN)("OpenShellRuntime (real gateway)", () => {
  const id = "oss-rt-e2e-slack";
  let bundleDir: string;

  afterAll(async () => {
    if (bundleDir) {
      cmdOk("openshell", ["sandbox", "delete", id]);
      await rm(bundleDir, { recursive: true, force: true });
    }
  });

  it(
    "boots a sandbox, uploads the slack bundle, and register() returns its kinds",
    async () => {
      bundleDir = await mkdtemp(path.join(tmpdir(), "oss-rt-e2e-"));
      const workspace = path.join(bundleDir, id);
      await mkdir(workspace, { recursive: true });
      await copyFile(SLACK_BUNDLE, path.join(workspace, "run.js"));

      const rt = new OpenShellRuntime({
        image: BASE_IMAGE,
        bundleDir,
        gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
        onLog: () => {},
      });

      await rt.start({
        id,
        hostWorkspacePath: workspace,
        network: "public",
        hosts: ["slack.com"],
      });

      const res = await rt.callRpc(id, "register", "{}", { timeoutMs: 60_000 });
      expect(res.ok).toBe(true);
      if (!res.ok) return;

      const result = res.result as {
        pluginName: string;
        capabilities: { action?: { kinds: Array<{ kind: string }> } };
      };
      expect(result.pluginName).toBe("@open-neko/plugin-slack");
      const kinds = (result.capabilities.action?.kinds ?? []).map((k) => k.kind);
      expect(kinds).toContain("send_slack_message");

      await rt.stop(id);
    },
    240_000,
  );
});
