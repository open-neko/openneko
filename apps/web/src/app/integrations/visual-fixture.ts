import { readdir, readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { parseManifest } from "@neko/packs/manifest";
import { parse } from "yaml";

// Visual testing uses the actual bundled declarations; only account state is synthetic.
export async function personalConnectionsFixture(state: string) {
  const root = resolve(process.cwd(), "../../packs");
  const entries = await readdir(root, { withFileTypes: true });
  const packs = await Promise.all(entries.filter(entry => entry.isDirectory() && entry.name !== "schema").map(entry => readFile(resolve(root, entry.name, "pack.yaml"), "utf8").then(raw => ({ manifest: parseManifest(parse(raw)) }))));
  return packs.flatMap(pack => pack.manifest.oauth.filter(connection => connection.scope === "user").map(connection => ({
    packId: pack.manifest.metadata.id,
    key: connection.key,
    providerLabel: connection.providerLabel,
    experience: connection.experience,
    scopes: connection.scopes,
    configured: state !== "unconfigured",
    connected: state === "connected",
    accountLabel: state === "connected" ? "synthetic-user@example.test" : null,
  })));
}
