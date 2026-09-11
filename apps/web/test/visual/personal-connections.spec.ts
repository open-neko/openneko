import { expect, test } from "@playwright/test";

test("personal connection cards keep aligned controls and accessible account actions", async ({ page }, testInfo) => {
  let accountRequests = 0;
  await page.route("**/api/my/pack-accounts/**", route => { accountRequests++; return route.abort(); });
  await page.goto("/integrations?state=connected");
  const card = page.getByRole("region", { name: "My connections" }).locator('[data-slot="card"]').first();
  const reconnect = card.getByRole("button", { name: "Reconnect", exact: true });
  const more = card.getByRole("button", { name: /^More actions for/ });
  for (const width of [1280, 696, 390]) {
    await page.setViewportSize({ width, height: 960 });
    await expect(reconnect).toBeVisible();
    const geometry = await card.evaluate(el => {
      const title = el.querySelector('h3')!.getBoundingClientRect();
      const buttons = [...el.querySelectorAll('[data-ui-action-group] button')].map(button => button.getBoundingClientRect());
      const disclosure = el.querySelector('details')!.getBoundingClientRect();
      return { titleX: title.x, actionX: buttons[0].x, disclosureX: disclosure.x, heights: buttons.map(button => button.height), tops: buttons.map(button => button.y), overflow: document.documentElement.scrollWidth > innerWidth };
    });
    expect(geometry.overflow).toBe(false);
    expect(Math.abs(geometry.titleX - geometry.actionX)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.titleX - geometry.disclosureX)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.tops[0] - geometry.tops[1])).toBeLessThanOrEqual(1);
    expect(geometry.heights[0]).toBe(geometry.heights[1]);
    expect(geometry.heights[0]).toBeGreaterThanOrEqual(width <= 720 ? 44 : 40);
    await more.focus();
    expect(await more.evaluate(el => { const style = getComputedStyle(el); return el === document.activeElement && (style.outlineStyle !== "none" || style.boxShadow !== "none"); })).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`personal-connected-${width}.png`), fullPage: true });
  }
  await card.getByText("Permissions and help", { exact: true }).click();
  const help = card.getByRole("link", { name: "Connection help" });
  await expect(help).toBeVisible();
  const style = (el: Element) => { const s = getComputedStyle(el); return { height: el.getBoundingClientRect().height, font: s.fontFamily, size: s.fontSize, weight: s.fontWeight, radius: s.borderRadius, padding: s.padding }; };
  expect(await reconnect.evaluate(style)).toEqual(await help.evaluate(style));
  const helpBox = await help.boundingBox();
  const scopesBox = await card.locator("details ul").boundingBox();
  expect(await card.locator("details ul").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
  expect(helpBox!.y - (scopesBox!.y + scopesBox!.height)).toBeGreaterThanOrEqual(16);
  await page.screenshot({ path: testInfo.outputPath("personal-expanded-390.png"), fullPage: true });
  await expect(reconnect).toBeDisabled();
  await expect(page.getByText("Visual preview. Account actions are disabled.")).toBeVisible();
  await more.click();
  await expect(page.getByRole("menuitem", { name: "Disconnect", exact: true })).toBeDisabled();
  await page.keyboard.press("Escape");
  expect(accountRequests).toBe(0);
  await page.getByRole("searchbox", { name: "Search integrations" }).fill("no-such-provider");
  await expect(page.getByRole("heading", { name: "No matching integrations" })).toBeVisible();
  await page.goto("/integrations?state=unconfigured");
  await expect(page.getByRole("button", { name: "Connect account", exact: true })).toBeDisabled();
});
