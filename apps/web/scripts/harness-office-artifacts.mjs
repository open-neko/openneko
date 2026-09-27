import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { chromium } from "@playwright/test";

const threadId = process.argv[2];
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ acceptDownloads: true });
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  for (const name of ["leads.xlsx", "summary.docx"]) {
    const link = page.locator(`a[title$="/process-1/${name}"]`);
    await link.waitFor({ timeout: 60_000 });
    assert.equal(await link.count(), 1, `${name} duplicated on first load`);
  }
  await page.reload({ waitUntil: "domcontentloaded" });
  for (const name of ["leads.xlsx", "summary.docx"]) {
    const link = page.locator(`a[title$="/process-1/${name}"]`);
    await link.waitFor({ timeout: 60_000 });
    assert.equal(await link.count(), 1, `${name} duplicated or lost after reload`);
    const downloadPromise = page.waitForEvent("download");
    await link.click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), name);
    const bytes = await readFile(await download.path());
    assert.equal(bytes.subarray(0, 4).toString(), "PK\u0003\u0004", `${name} is not an Office ZIP`);
  }
  console.log("M5_BROWSER_OFFICE_ARTIFACTS_PASS", threadId);
} finally {
  await browser.close();
}
