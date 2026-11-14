import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ConnectorCredential } from "@neko/secrets";
import { CoreSecrets } from "../../src/secrets/core-secrets";
import { SignInService } from "../../src/signin/signin-service";

let dir: string;
let secrets: CoreSecrets;

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "signin-service-"));
  secrets = new CoreSecrets({ configDir: dir, pollMs: 0 });
  await secrets.start();
});

afterEach(async () => {
  secrets.stop();
  await rm(dir, { recursive: true, force: true });
});

function credential(tokens: Record<string, unknown>): ConnectorCredential {
  return { tokens, connectedAt: "2026-01-01T00:00:00.000Z" };
}

describe("SignInService directory and Scalekit settings", () => {
  it("offers the directory only while Scalekit is the provider", async () => {
    const signIn = new SignInService(secrets);
    expect(signIn.getDirectoryProvider()).toBeNull();
    await signIn.selectProvider("scalekit");
    expect(signIn.getDirectoryProvider()?.capabilities.write).toEqual({ createUser: true, deactivateUser: false });
    await expect(signIn.applyDirectoryChange({ op: "deactivate_user", externalId: "u1" })).rejects.toThrow(/does not accept/);
  });
});

describe("Scalekit workspace connection", () => {
  it("stores the workspace credential in the deployment slot and refreshes it before a tool call", async () => {
    const refreshed: string[] = [];
    const seenTokens: string[] = [];
    const signIn = new SignInService(secrets, {
      scalekit: {
        createMcpOAuth: () => ({
          refreshTokens: async () => {
            refreshed.push("x");
            return { access_token: "at-new", refresh_token: "rt-new", expires_in: 3600 };
          },
        }) as never,
        createMcpClient: async ({ accessToken }) => ({
          callTool: async () => {
            seenTokens.push(accessToken);
            return { content: [{ type: "text", text: "ok" }] };
          },
          close: async () => {},
        }),
      },
    });
    expect(signIn.getDeploymentConnectStatus()).toEqual([]);
    await secrets.setOperatorCredential("deployment", "scalekit", credential({
      access_token: "at-old",
      refresh_token: "rt-old",
      expires_at: "0",
      token_endpoint: "https://as.scalekit.com/token",
      client_id: "dcr-1",
    }));
    expect(signIn.getDeploymentConnectStatus()).toEqual([
      { pluginName: "scalekit", connectedAt: "2026-01-01T00:00:00.000Z", scopes: undefined },
    ]);

    expect(await signIn.callWorkspaceTool("list_environments", {})).toBe("ok");
    expect(refreshed).toHaveLength(1);
    expect(seenTokens).toEqual(["at-new"]);
    expect(secrets.operatorCredential("deployment", "scalekit")?.tokens.access_token).toBe("at-new");

    expect(await signIn.disconnect("scalekit", "anyone")).toBe(true);
    expect(signIn.getDeploymentConnectStatus()).toEqual([]);
  });
});
