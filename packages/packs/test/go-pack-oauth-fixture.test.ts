import { readFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { loadSolutionPack } from "../src/index.js";
import { buildPackAuthorizationUrl } from "../src/oauth-client.js";

// The Go admin pack OAuth flow (apps/openneko/internal/webfront) must build the same URL.
// Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = fileURLToPath(new URL("../../../apps/openneko/internal/webfront/testdata/packoauth.json", import.meta.url));
const PACKS = fileURLToPath(new URL("../../../packs", import.meta.url));

describe("Go pack OAuth fixture", () => {
  it("matches the authorization URL of every built-in OAuth connection", async () => {
    const urls: Record<string, string> = {};
    for (const packId of ["google-workspace"]) {
      const bundle = await loadSolutionPack(`${PACKS}/${packId}`);
      for (const connection of bundle.manifest.oauth) {
        urls[`${packId}/${connection.key}`] = buildPackAuthorizationUrl({
          connection, clientId: "client id+1", redirectUri: "https://neko.example/api/pack-accounts/x/y/callback",
          state: "state~1", codeChallenge: "challenge_-1",
        });
      }
    }
    const fixture = `${JSON.stringify(urls, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(await readFile(FIXTURE, "utf8"));
  });
});
