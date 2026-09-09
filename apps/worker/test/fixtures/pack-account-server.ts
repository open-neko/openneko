// Live browser fixture. No plugins, mock runner, real accounts or provider tokens.
import { createServer } from "node:http";
import { mkdtemp, mkdir, writeFile, cp, rm } from "node:fs/promises";
import { join } from "node:path";
import { homedir, tmpdir } from "node:os";
import { stringify } from "yaml";
import { pool } from "@neko/db";
import { PackService } from "../../src/packs/service";
import { createAdminHandler } from "../../src/admin-server";

const image = process.env.OPENNEKO_PACK_EXEC_TEST_IMAGE;
if (!image) throw new Error("Set OPENNEKO_PACK_EXEC_TEST_IMAGE to the built fixture digest");
const root = await mkdtemp(join(tmpdir(), "pack-browser-"));
await cp(join(process.env.XDG_CONFIG_HOME || join(homedir(), ".config"), "openshell"), join(root, "config/openshell"), { recursive: true });
process.env.XDG_CONFIG_HOME = join(root, "config");
const org = `pack-browser-${Date.now()}`;
await pool().query("insert into organization(id,name) values($1,'Pack browser fixture')", [org]);
const manifest = {
  apiVersion: "openneko.app/v1", kind: "SolutionPack",
  metadata: { id: "account-fixture", name: "Account fixture", version: "1.0.0", publisher: "test", category: "operations" },
  compatibility: { openneko: ">=2.40.0", applications: [], databases: [] }, inputs: [], secrets: [], artifacts: { skills: [] },
  health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} },
  connectors: [{ id: "fixture", image, entrypoint: "/app/connector", operations: [{ id: "echo", description: "Echo", effect: "read" }], network: [], auth: { label: "Test pack account", authorizationOrigin: "https://provider.example", scopes: ["read"], credentialVersion: "1" } }],
};
await mkdir(join(root, "packs/account-fixture"), { recursive: true });
await writeFile(join(root, "packs/account-fixture/pack.yaml"), stringify(manifest));
const packs = new PackService(org, join(root, "packs"));
const review = await packs.review("account-fixture");
await packs.install("account-fixture", { reviewHash: review.reviewHash });
const handler = createAdminHandler({ packs });
const server = createServer((req, res) => {
  const accountId = /^\/fixture\/read\/([a-f0-9-]{36})$/.exec(req.url ?? "")?.[1];
  if (accountId) {
    void packs.runConnector("account-fixture", "fixture", "echo", { value: "browser" }, { ownerId: "solo", accountId }).then(result => {
      res.setHeader("content-type", "application/json"); res.end(JSON.stringify(result));
    }).catch(() => { res.statusCode = 400; res.end("Account read failed"); });
    return;
  }
  handler(req, res);
});
server.listen(4113, "127.0.0.1", () => console.log("Pack browser fixture ready on 4113"));
let closing = false;
async function close() {
  if (closing) return; closing = true;
  server.close();
  await packs.uninstall("account-fixture");
  await pool().query("delete from organization where id=$1", [org]);
  await pool().end(); await rm(root, { recursive: true, force: true }); process.exit(0);
}
process.on("SIGTERM", () => void close());
process.on("SIGINT", () => void close());
