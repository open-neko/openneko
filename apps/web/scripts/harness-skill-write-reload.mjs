import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const [createdThread, updatedThread] = process.argv.slice(2);
assert.match(createdThread ?? "", /^[a-f0-9-]{36}$/);
assert.match(updatedThread ?? "", /^[a-f0-9-]{36}$/);

const browser = await chromium.launch({ headless: true });
try {
  for (const [threadId, answer] of [
    [createdThread, "Created the fixture-lead-review skill."],
    [updatedThread, "Updated the fixture-lead-review skill."],
  ]) {
    const page = await browser.newPage();
    try {
      await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
      const result = page.getByText(answer, { exact: true });
      await result.waitFor({ timeout: 60_000 });
      assert.equal(await result.count(), 1, "skill write answer duplicated on first load");
      await page.reload({ waitUntil: "domcontentloaded" });
      await result.waitFor({ timeout: 60_000 });
      assert.equal(await result.count(), 1, "skill write answer duplicated or lost after reload");
    } finally {
      await page.close();
    }
  }
  console.log("M5_WEB_SKILL_WRITE_PASS", createdThread, updatedThread);
} finally {
  await browser.close();
}
