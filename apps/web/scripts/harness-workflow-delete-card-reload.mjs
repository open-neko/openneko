import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const threadId = process.argv[2];
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  const deleted = page.getByText("is gone, along with its triggers, run history, and proposed actions.", { exact: false });
  await deleted.waitFor({ timeout: 60_000 });
  assert.equal(await deleted.count(), 1, "workflow deletion card duplicated on first load");
  await page.reload({ waitUntil: "domcontentloaded" });
  await deleted.waitFor({ timeout: 60_000 });
  assert.equal(await deleted.count(), 1, "workflow deletion card duplicated or lost after reload");
  console.log("M5_WEB_WORKFLOW_DELETE_CARD_PASS", threadId);
} finally {
  await browser.close();
}
