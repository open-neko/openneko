import { expect, test, type Page } from "@playwright/test";
import { FIXTURE as F } from "../../../../packages/contract/src/fixtures";

const screens = [
  "/chat",
  `/chat/t/${F.adminThreadId}`,
  "/chat/bots/new",
  "/work",
  `/work/${F.adminThreadId}`,
  "/workflows",
  `/workflows?id=${F.adminWorkflowId}`,
  "/library",
  "/memory",
  "/skills",
  "/profile",
  "/actions",
  `/actions/${F.adminActionRequestId}`,
  "/business-profile",
  "/runs",
  `/runs/${F.adminWorkflowRunId}`,
  "/admin",
  "/admin/rules",
  "/admin/settings",
  "/admin/settings/channels",
  "/admin/settings/packs",
  "/admin/settings/signin",
  "/admin/settings/agent",
  "/admin/settings/data",
  "/admin/settings/models",
  "/admin/settings/research",
  "/admin/settings/security",
  "/admin/settings/spend",
  "/admin/settings/sso",
  "/admin/settings/workflows",
  "/admin/settings/graphjin",
  "/admin/users",
];

async function contentPastEdge(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const width = document.documentElement.clientWidth;
    const inside = (b: DOMRect) => b.left >= -1 && b.right <= width + 1;
    const found: string[] = [];
    for (const el of document.querySelectorAll("body *")) {
      const box = el.getBoundingClientRect();
      if (box.width === 0 || box.height === 0 || inside(box)) continue;
      let scrolls = false;
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        if (/(auto|scroll|hidden|clip)/.test(getComputedStyle(p).overflowX) && inside(p.getBoundingClientRect())) {
          scrolls = true;
          break;
        }
      }
      if (!scrolls) found.push(`${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} "${(el.textContent ?? "").trim().slice(0, 40)}"`);
    }
    return found;
  });
}

for (const width of [400, 360, 1280]) {
  test.describe(`${width} px`, () => {
    test.use({ viewport: { width, height: 860 } });
    for (const path of screens) {
      test(path, async ({ page }) => {
        await page.goto(path, { waitUntil: "networkidle" });
        await page.waitForTimeout(400);
        expect(new URL(page.url()).pathname.startsWith(path.split("?")[0])).toBe(true);
        await expect(page.getByText("Page not found")).toHaveCount(0);
        await page.evaluate(() => window.scrollTo(10_000, window.scrollY));
        expect(await page.evaluate(() => window.scrollX)).toBe(0);
        expect(await contentPastEdge(page)).toEqual([]);
      });
    }
  });
}
