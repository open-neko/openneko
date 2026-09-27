import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const threadId = process.argv[2];
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  const watch = page.getByText("Checks references.0.id with gt every 60 seconds.", { exact: false });
  await watch.waitFor({ timeout: 60_000 });
  assert.equal(await watch.count(), 1, "condition watch card duplicated on first load");
  await page.reload({ waitUntil: "domcontentloaded" });
  await watch.waitFor({ timeout: 60_000 });
  assert.equal(await watch.count(), 1, "condition watch card duplicated or lost after reload");
  console.log("M5_WEB_WORKFLOW_TRIGGER_CARD_PASS", threadId);
} finally {
  await browser.close();
}
