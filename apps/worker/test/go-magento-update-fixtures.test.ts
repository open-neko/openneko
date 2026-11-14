import { readFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { DEFAULT_MAGENTO_ATTRIBUTE_CLASSIFICATIONS, loadSolutionPack } from "@neko/packs";
import { magentoGraphjinUpdate } from "../src/packs/magento-graphjin";
import type { MagentoPreflightResult } from "../src/packs/magento-preflight";

// The Go apply (apps/openneko/internal/webfront/packmagentoapply.go) must build
// the same Magento GraphJin update and classifications. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite it.
const FIXTURE = fileURLToPath(new URL("../../openneko/internal/webfront/testdata/nodemagentoupdate.json", import.meta.url));
const PACKS = fileURLToPath(new URL("../../../packs", import.meta.url));

describe("Go Magento update fixture", () => {
  it("matches the Magento GraphJin update", async () => {
    const bundle = await loadSolutionPack(join(PACKS, "magento"));
    const inputs = { "database.host": "db.shop", "database.port": 3306, "database.name": "magento", "magento.base_url": "https://shop.test" };
    const preflight = {
      databaseType: "mariadb",
      tablePrefix: "m2_",
      availableAnalyticsTables: ["sales_order", "sales_order_item", "customer_entity", "store", "catalog_product_entity"],
      blockedTables: ["m2_admin_user", "m2_oauth_token"],
    } as unknown as MagentoPreflightResult;
    const updates = [
      { name: "read-only", secrets: { "database.analytics_username": "ro", "database.analytics_password": "pw" }, retired: [] as string[] },
      { name: "write", secrets: { "database.analytics_username": "ro", "database.analytics_password": "pw", "magento.integration_token": "tok" }, retired: ["old_source"] },
    ].map(c => ({ ...c, inputs, preflight, update: magentoGraphjinUpdate(inputs, c.secrets, preflight, bundle, c.retired) }));
    const fixture = `${JSON.stringify({ classifications: DEFAULT_MAGENTO_ATTRIBUTE_CLASSIFICATIONS, updates }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(await readFile(FIXTURE, "utf8")).toBe(fixture);
  });
});
