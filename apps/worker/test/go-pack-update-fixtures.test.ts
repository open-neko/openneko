import { readFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { loadSolutionPack } from "@neko/packs";
import { declarativeGraphjinUpdate } from "../src/packs/declarative";
import { graphjinApiArtifacts } from "../src/packs/materialize";
import { resolveInputs } from "../src/packs/configuration";

// The Go apply (apps/openneko/internal/webfront/packmaterialize.go) must build
// the same GraphJin update. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite it.
const FIXTURE = fileURLToPath(new URL("../../openneko/internal/webfront/testdata/nodepackupdate.json", import.meta.url));
const PACKS = fileURLToPath(new URL("../../../packs", import.meta.url));

describe("Go pack update fixture", () => {
  it("matches the declarative GraphJin update of each built-in pack", async () => {
    const cases = [];
    const supplied: Record<string, Record<string, unknown>> = {
      shopify: { "shopify.store_url": "https://acme.myshopify.com/" },
      resend: { "resend.from": "Acme Ops <ops@mail.acme.com>" },
      "google-workspace": { "google-workspace.oauth_client_id": "client-1" },
    };
    for (const id of ["slack", "shopify", "resend", "parallel-search", "google-workspace"]) {
      const bundle = await loadSolutionPack(join(PACKS, id));
      const inputs = resolveInputs(bundle, supplied[id] ?? {});
      const secrets = Object.fromEntries(bundle.manifest.secrets.map(secret => [secret.key, `secret-${secret.key}`]));
      const api = await graphjinApiArtifacts({ bundle, values: inputs });
      for (const mode of ["files", "api", "public"] as const) {
        const update = declarativeGraphjinUpdate(bundle, inputs, secrets, mode === "files" ? ["old_source"] : [], {},
          mode === "files" ? undefined : api, mode === "public" ? "public" : "authenticated");
        cases.push({ pack: id, mode, inputs, secrets, update });
      }
    }
    const fixture = `${JSON.stringify(cases, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(await readFile(FIXTURE, "utf8")).toBe(fixture);
  });
});
