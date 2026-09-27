import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const threadId = process.argv[2];
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  const saved = page.getByText("Auto-approve synthetic low-risk changes", { exact: true });
  await saved.waitFor({ timeout: 60_000 });
  assert.equal(await saved.count(), 1, "edited rule card duplicated on first load");
  await page.reload({ waitUntil: "domcontentloaded" });
  await saved.waitFor({ timeout: 60_000 });
  assert.equal(await saved.count(), 1, "edited rule card duplicated or lost after reload");
  console.log("M5_WEB_RULE_CARD_PASS", threadId);
} finally {
  await browser.close();
}
