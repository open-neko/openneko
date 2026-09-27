import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { chromium } from "@playwright/test";

const threadId = process.argv[2];
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ acceptDownloads: true });
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  const link = page.locator('a[title$="/process-1/result.csv"]');
  await link.waitFor({ timeout: 60_000 });
  assert.equal(await link.count(), 1, "isolated artifact duplicated on first load");
  await page.reload({ waitUntil: "domcontentloaded" });
  await link.waitFor({ timeout: 60_000 });
  assert.equal(await link.count(), 1, "isolated artifact duplicated or lost after reload");
  const downloadPromise = page.waitForEvent("download");
  await link.click();
  const download = await downloadPromise;
  assert.equal(download.suggestedFilename(), "result.csv");
  assert.equal(await readFile(await download.path(), "utf8"), "lead_id\nLEAD-42\n");
  console.log("M5_BROWSER_PROCESS_ARTIFACT_PASS", threadId);
} finally {
  await browser.close();
}
