import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const threadId=process.argv[2];
assert.match(threadId ?? "",/^[a-f0-9-]{36}$/);
const browser=await chromium.launch({headless:true});
try {
  const page=await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`,{waitUntil:"domcontentloaded"});
  const answer=page.getByText("Adding the administrator to Harness Reviewers is pending approval.",{exact:true});
  const request=page.locator(".work-action-row").filter({hasText:"Add the administrator to Harness Reviewers"});
  for(let attempt=0;attempt<2;attempt++) {
    await answer.waitFor({timeout:60_000});
    await request.getByText("Done",{exact:true}).waitFor({timeout:60_000});
    assert.equal(await answer.count(),1,"group-member answer duplicated");
    assert.equal(await request.count(),1,"group-member approval card duplicated");
    if(attempt===0) await page.reload({waitUntil:"domcontentloaded"});
  }
  console.log("M5_WEB_GROUP_MEMBER_PASS",threadId);
} finally {
  await browser.close();
}
