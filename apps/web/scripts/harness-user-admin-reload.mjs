import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const threadId=process.argv[2];
assert.match(threadId ?? "",/^[a-f0-9-]{36}$/);
const browser=await chromium.launch({headless:true});
try {
  const page=await browser.newPage();
  await page.goto(`http://localhost:18121/work/${threadId}`,{waitUntil:"domcontentloaded"});
  const answer=page.getByText("The synthetic team invitation is pending administrator approval.",{exact:true});
  const request=page.locator(".work-action-row").filter({hasText:"Invite the synthetic team member"});
  for(let attempt=0;attempt<2;attempt++) {
    await answer.waitFor({timeout:60_000});
    await request.getByText("Done",{exact:true}).waitFor({timeout:60_000});
    assert.equal(await answer.count(),1,"user-admin answer duplicated");
    assert.equal(await request.count(),1,"user-admin approval card duplicated");
    if(attempt===0) await page.reload({waitUntil:"domcontentloaded"});
  }
  console.log("M5_WEB_USER_ADMIN_PASS",threadId);
} finally {
  await browser.close();
}
