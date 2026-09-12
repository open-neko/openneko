import { execSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import { makeAgentBackend } from "../src/agent-backend-resolver";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import { ensureWorkWorkspace } from "../src/work/workspace";

/**
 * Live agent-in-sandbox e2e. OPT-IN — skipped unless OPENNEKO_OPENSHELL_E2E=1
 * AND the `openshell` CLI is on PATH AND a gateway/provider/agent-image are set
 * up (the same env the worker/web use). Drives the real launcher
 * (makeSandboxRunCore → create → upload → entry.ts → runAgentBackend → hermes →
 * model) and asserts a real, non-empty completion — the path proven by hand on
 * 2026-06-03 (hermes/gemini → "PONG", key never in the box).
 *
 * Run: OPENNEKO_OPENSHELL_E2E=1 OPENNEKO_AGENT_IMAGE=… OPENNEKO_AGENT_MODEL_PROVIDER=… \
 *      OPENNEKO_AGENT_MODEL_HOST=…,… \
 *      OPENNEKO_AGENT_MODEL_KEY_ENV=… OPENNEKO_AGENT_HERMES_HOME=… \
 *      pnpm --filter @neko/llm exec vitest run test/sandbox-launcher-e2e.test.ts
 */
function hasOpenshell(): boolean {
  try {
    execSync("which openshell", { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

const ENABLED =
  process.env.OPENNEKO_OPENSHELL_E2E === "1" &&
  Boolean(process.env.OPENNEKO_AGENT_IMAGE) &&
  Boolean(process.env.OPENNEKO_AGENT_MODEL_PROVIDER) &&
  hasOpenshell();

const d = ENABLED ? describe : describe.skip;
const delegationIt =
  ENABLED && process.env.OPENNEKO_AGENT_DELEGATION_E2E === "1" ? it : it.skip;

d("agent-in-sandbox e2e (opt-in)", () => {
  it(
    "runs a hermes turn in a fresh OpenShell sandbox and returns real text",
    async () => {
      const hosts = (process.env.OPENNEKO_AGENT_MODEL_HOST ?? "")
        .split(",")
        .map((h) => h.trim())
        .filter(Boolean);
      const keyEnv = process.env.OPENNEKO_AGENT_MODEL_KEY_ENV;
      const runId = `e2e-${process.pid}-${process.hrtime.bigint()}`;
      const orgId = "openshell-e2e-org";
      const workspace = await ensureWorkWorkspace(orgId, "e2e-thread", runId);

      const runCore = makeSandboxRunCore({
        warmPoolSize: 0, // This suite explicitly exercises disposable cold creation.
        agentImage: process.env.OPENNEKO_AGENT_IMAGE!,
        gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
        gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
        modelProvider: process.env.OPENNEKO_AGENT_MODEL_PROVIDER,
        modelHosts: hosts.map((host) => ({ host })),
        keyAliases: keyEnv ? [{ from: "api_key", to: keyEnv }] : undefined,
        hermesHomeHostPath: process.env.OPENNEKO_AGENT_HERMES_HOME || undefined,
        execTimeoutMs: 240_000,
        onLog: () => {},
      });

      const result = await runCore({
        backend: makeAgentBackend({ id: "hermes" }),
        prompt: "You are a terse test assistant.",
        userMessage: "Reply with the single word PONG and nothing else.",
        orgId,
        threadId: "e2e-thread",
        runId,
        workspace,
        pluginActions: [],
        emit: () => Promise.resolve(),
      });

      expect(result.status).toBe("completed");
      // The prompt demands exactly "PONG", so a real model turn returns it.
      // Assert the REAL content AND reject error-as-success: an egress / proxy /
      // model failure surfaces as error text in finalText (e.g. "API call failed
      // after 3 retries: HTTP 404"), which a bare non-empty check would let
      // pass — that gap let a broken model call through a "green" run.
      expect(result.finalText).toMatch(/PONG/i);
      expect(result.finalText).not.toMatch(
        /API call failed|HTTP \d{3}|\berror\b|denied|not found|placeholder/i,
      );
    },
    300_000,
  );

  delegationIt(
    "runs Hermes delegate_task inside the same run sandbox and pulls back child artifacts",
    async () => {
      const hosts = (process.env.OPENNEKO_AGENT_MODEL_HOST ?? "")
        .split(",")
        .map((h) => h.trim())
        .filter(Boolean);
      const keyEnv = process.env.OPENNEKO_AGENT_MODEL_KEY_ENV;
      const runId = `delegation-e2e-${process.pid}-${process.hrtime.bigint()}`;
      const orgId = "openshell-delegation-e2e-org";
      const workspace = await ensureWorkWorkspace(orgId, "delegation-thread", runId);
      const marker = `CHILD_OK_${runId}`;

      const runCore = makeSandboxRunCore({
        warmPoolSize: 0, // This suite explicitly exercises disposable cold creation.
        agentImage: process.env.OPENNEKO_AGENT_IMAGE!,
        gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
        gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
        modelProvider: process.env.OPENNEKO_AGENT_MODEL_PROVIDER,
        modelHosts: hosts.map((host) => ({ host })),
        keyAliases: keyEnv ? [{ from: "api_key", to: keyEnv }] : undefined,
        hermesHomeHostPath: process.env.OPENNEKO_AGENT_HERMES_HOME || undefined,
        execTimeoutMs: 360_000,
        onLog: () => {},
      });
      const events: unknown[] = [];

      const result = await runCore({
        backend: makeAgentBackend({ id: "hermes" }),
        prompt: `You are testing Hermes native delegation. You must call delegate_task exactly once. The delegated child must use a terminal/file-capable toolset to write exactly ${marker} to ${workspace.artifactRoot}/delegation-child.txt. After the child reports success, reply with exactly DELEGATION_DONE and nothing else.`,
        userMessage: "Run the delegation test now.",
        orgId,
        threadId: "delegation-thread",
        runId,
        workspace,
        pluginActions: [],
        emit: (event) => {
          events.push(event);
          return Promise.resolve();
        },
      });

      expect(result.status).toBe("completed");
      expect(result.finalText).toMatch(/DELEGATION_DONE/i);
      expect(
        events.some(
          (event) =>
            typeof event === "object" &&
            event !== null &&
            (event as { type?: unknown; name?: unknown }).type === "tool_start" &&
            /delegate_task/i.test(String((event as { name?: unknown }).name ?? "")),
        ),
      ).toBe(true);
      await expect(
        readFile(`${workspace.artifactRoot}/delegation-child.txt`, "utf8"),
      ).resolves.toContain(marker);
    },
    420_000,
  );
});
