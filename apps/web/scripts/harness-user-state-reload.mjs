import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const [threadId, action] = process.argv.slice(2);
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
assert.ok(["deactivate", "reactivate", "promote"].includes(action));
const verb = { deactivate: "Deactivating", reactivate: "Reactivating", promote: "Promoting" }[action];
const summary = { deactivate: "Deactivate", reactivate: "Reactivate", promote: "Promote" }[action];
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  const answer = page.getByText(`${verb} the synthetic member is pending administrator approval.`, { exact: true });
  const request = page.locator(".work-action-row").filter({ hasText: `${summary} the synthetic member` });
  for (let attempt = 0; attempt < 2; attempt++) {
    await answer.waitFor({ timeout: 60_000 });
    await request.getByText("Done", { exact: true }).waitFor({ timeout: 60_000 });
    assert.equal(await answer.count(), 1, "user-state answer duplicated");
    assert.equal(await request.count(), 1, "user-state approval card duplicated");
    if (attempt === 0) await page.reload({ waitUntil: "domcontentloaded" });
  }
  console.log(`M5_WEB_USER_${action.toUpperCase()}_PASS`, threadId);
} finally {
  await browser.close();
}
