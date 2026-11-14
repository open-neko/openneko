import { expect, test, type Page } from "@playwright/test";
import { FIXTURE as F } from "../../../../packages/contract/src/fixtures";
import { cardThreads } from "./cards";

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
  "/admin/users/groups/c0a7ac70-0000-4000-8000-000000000061",
  `/admin/access?type=workflow&id=${F.adminWorkflowId}`,
  "/device",
  "/integrations",
  "/chat/connectors",
  "/onboarding",
  "/admin/settings/packs/magento/operations",
  ...cardThreads.map((card) => `/work/${card.threadId}`),
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

async function clippedText(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const width = document.documentElement.clientWidth;
    const found: string[] = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const owner = node.parentElement!;
      if (!(node.textContent ?? "").trim() || owner.closest("input, textarea, select, svg")) continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      const text = range.getBoundingClientRect();
      if (text.width === 0) continue;
      let edge = width;
      let ok = false;
      for (let p: Element | null = owner; p && p !== document.documentElement; p = p.parentElement) {
        const style = getComputedStyle(p);
        if (style.textOverflow === "ellipsis" || p.clientWidth <= 2) {
          ok = true;
          break;
        }
        if (/(auto|scroll)/.test(style.overflowX)) {
          ok = true;
          break;
        }
        if (/(hidden|clip)/.test(style.overflowX) && p !== document.body) {
          edge = Math.min(edge, p.getBoundingClientRect().right);
          break;
        }
      }
      if (!ok && text.left < edge - 4 && text.right > edge + 1) {
        found.push(`${owner.tagName.toLowerCase()}.${String(owner.className).slice(0, 60)} "${(node.textContent ?? "").trim().slice(0, 40)}"`);
      }
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
        expect(await clippedText(page)).toEqual([]);
      });
    }
  });
}

test.describe("answer cards at 400 px", () => {
  test.use({ viewport: { width: 400, height: 860 } });
  for (const card of cardThreads) {
    test(`${card.key}: tables scroll inside the card and words stay whole`, async ({ page }) => {
      await page.goto(`/work/${card.threadId}`, { waitUntil: "networkidle" });
      await page.waitForTimeout(400);
      const problems = await page.evaluate(() => {
        const found: string[] = [];
        for (const surface of document.querySelectorAll(".work-surface")) {
          const card = surface.getBoundingClientRect();
          for (const table of surface.querySelectorAll("table, .answer-compare")) {
            let box: Element | null = table;
            while (box && box !== surface && !/(auto|scroll)/.test(getComputedStyle(box).overflowX)) box = box.parentElement;
            const wide = table.scrollWidth > card.width + 1;
            if (!box || box === surface) {
              if (wide) found.push(`no scroll box: ${String(table.className).slice(0, 40)}`);
              continue;
            }
            const b = box.getBoundingClientRect();
            if (b.left < card.left - 20 || b.right > card.right + 1) found.push(`scroll box outside the card: ${String(box.className).slice(0, 40)}`);
          }
          for (const cell of surface.querySelectorAll("td, th, dt, dd")) {
            const words = (cell.textContent ?? "").trim().split(/\s+/).filter((w) => w.length >= 4);
            if (words.length === 0) continue;
            const style = getComputedStyle(cell);
            const probe = document.createElement("span");
            probe.style.cssText = `position:absolute;visibility:hidden;white-space:nowrap;font:${style.font};letter-spacing:${style.letterSpacing}`;
            document.body.append(probe);
            const longest = Math.max(...words.map((w) => {
              probe.textContent = w;
              return probe.getBoundingClientRect().width;
            }));
            probe.remove();
            const inner = cell.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
            if (inner + 1 < longest) found.push(`word split in ${cell.tagName.toLowerCase()}: "${(cell.textContent ?? "").trim().slice(0, 40)}"`);
          }
        }
        return found;
      });
      expect(problems).toEqual([]);
    });
  }
});
