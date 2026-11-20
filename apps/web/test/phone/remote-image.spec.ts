import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { expect, test } from "@playwright/test";
import pg from "pg";
import { FIXTURE as F } from "../../../../packages/contract/src/fixtures";

function databaseUrl(): string {
  const file = process.env.OPENNEKO_DATABASE_URL_FILE;
  if (file) return readFileSync(file, "utf8").trim();
  if (process.env.DATABASE_URL) return process.env.DATABASE_URL.trim();
  throw new Error("set OPENNEKO_DATABASE_URL_FILE or DATABASE_URL (scripts/phone-local.sh)");
}

test.use({ viewport: { width: 400, height: 860 } });

test("an answer with a remote image makes no request to another host", async ({ page, baseURL }) => {
  const hits: string[] = [];
  const beacon = http.createServer((req, res) => {
    hits.push(req.url ?? "");
    res.writeHead(200, { "content-type": "image/png" });
    res.end();
  });
  await new Promise<void>((resolve) => beacon.listen(0, "127.0.0.1", resolve));
  const outside = `http://127.0.0.1:${(beacon.address() as AddressInfo).port}`;

  const thread = randomUUID();
  const client = new pg.Client({ connectionString: databaseUrl() });
  await client.connect();
  try {
    const { rows } = await client.query("select org_id from app_user where id = $1", [F.adminUserId]);
    await client.query("insert into work_thread (id, org_id, title, created_by_user_id) values ($1, $2, 'Remote image', $3)",
      [thread, rows[0].org_id, F.adminUserId]);
    await client.query(
      `insert into work_message (org_id, thread_id, role, content, created_at) values
       ($1, $2, 'user', 'Chart the revenue', now() - interval '1 minute'), ($1, $2, 'assistant', $3, now())`,
      [rows[0].org_id, thread, `Revenue is 49,310.\n\n![revenue chart](${outside}/p.png?d=49310)\n\n[your invoice](${outside}/c?d=49310)`],
    );
  } finally {
    await client.end();
  }

  const elsewhere: string[] = [];
  const own = new URL(baseURL!).origin;
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.protocol.startsWith("http") && url.origin !== own) elsewhere.push(request.url());
  });
  const response = await page.goto(`/work/${thread}`, { waitUntil: "networkidle" });
  await expect(page.getByText("Revenue is 49,310.")).toBeVisible();
  await page.waitForTimeout(500);
  expect(elsewhere).toEqual([]);
  expect(hits).toEqual([]);
  expect(response?.headers()["content-security-policy"]).toContain("img-src 'self' data: blob:");
  expect(response?.headers()["referrer-policy"]).toBe("no-referrer");

  const image = page.getByRole("link", { name: /Image: revenue chart/ });
  await expect(image).toBeVisible();
  await expect(image).toContainText("(127.0.0.1:");
  await expect(page.locator(`img[src^="${outside}"]`)).toHaveCount(0);
  const invoice = page.getByRole("link", { name: /your invoice/ });
  await expect(invoice).toHaveAttribute("rel", "noopener noreferrer");
  await expect(invoice).toContainText(`(127.0.0.1:`);

  const blocked = await page.evaluate(async (src) => {
    const violation = new Promise<string>((resolve) =>
      document.addEventListener("securitypolicyviolation", (e) => resolve(e.effectiveDirective), { once: true }));
    const img = document.createElement("img");
    img.src = src;
    document.body.appendChild(img);
    return Promise.race([violation, new Promise<string>((resolve) => setTimeout(() => resolve("none"), 3_000))]);
  }, `${outside}/injected.png?d=49310`);
  expect(blocked).toBe("img-src");

  await page.waitForTimeout(500);
  expect(elsewhere.filter((url) => !url.includes("/injected.png"))).toEqual([]);
  expect(hits).toEqual([]);
  beacon.close();
});
