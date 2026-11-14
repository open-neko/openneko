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

describe("Scalekit workspace connection", () => {
  it("stores and disconnects the workspace credential in the deployment slot", async () => {
    const signIn = new SignInService(secrets);
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

    expect(await signIn.disconnect("scalekit", "anyone")).toBe(true);
    expect(signIn.getDeploymentConnectStatus()).toEqual([]);
  });
});
