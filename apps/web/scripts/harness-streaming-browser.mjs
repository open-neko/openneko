import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "@playwright/test";

const [threadId, readyPath] = process.argv.slice(2);
assert.match(threadId ?? "", /^[a-f0-9-]{36}$/);
assert.ok(readyPath);

const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  const streamReady = page.waitForResponse(
    (response) => response.url().includes(`/threads/${threadId}/runs/`) &&
      response.url().endsWith("/events") && response.status() === 200,
    { timeout: 60_000 },
  );
  const pageResponse = await page.goto(`http://localhost:18121/work/${threadId}`, { waitUntil: "domcontentloaded" });
  assert.equal(pageResponse?.status(), 200, `Work page returned ${pageResponse?.status()}`);
  await streamReady;
  await writeFile(readyPath, "ready");

  const draft = page.getByText("Draft answer", { exact: true });
  await draft.waitFor({ timeout: 60_000 });
  assert.match(await draft.locator("..").innerText(), /STREAM-/);
  const draftAt = Date.now();

  const final = page.getByText("STREAM-OK", { exact: true });
  await final.waitFor({ timeout: 60_000 });
  await draft.waitFor({ state: "detached", timeout: 10_000 });
  assert.ok(Date.now() - draftAt >= 300, "draft was not visible before final answer");

  await page.reload({ waitUntil: "domcontentloaded" });
  await final.waitFor({ timeout: 60_000 });
  assert.equal(await draft.count(), 0, "draft survived terminal replay");
  console.log("M6_RENDERED_BROWSER_STREAMING_PASS", threadId);
} finally {
  await browser.close();
}
