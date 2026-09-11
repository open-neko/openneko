import { expect, test } from "@playwright/test";

const id = (index: number) => `11111111-1111-4111-8111-${String(index).padStart(12, "0")}`;
const date = "2026-09-01T00:00:00.000Z";

test("large Library browsing, semantic results and editing stay usable on desktop and phone", async ({ page }, testInfo) => {
  let savedTitle = "Inventory replenishment";
  let patches = 0;
  let conflict = false;
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.route("**/api/library?*", route => {
    const params = new URL(route.request().url()).searchParams;
    const documents = (params.get("view") ?? "documents") === "documents";
    const q = params.get("q") ?? "";
    if (q === "server-error") return route.fulfill({ status: 503, json: { error: "Library could not be loaded. Try again." } });
    const total = q === "no-match" ? 0 : q ? 1 : documents ? 125 : 1007;
    const current = Number(params.get("page") ?? 1);
    const rows = Array.from({ length: Math.min(50, Math.max(0, total - (current - 1) * 50)) }, (_, offset) => {
      const index = (current - 1) * 50 + offset;
      return { id: id(index), title: index === 0 ? savedTitle : `Synthetic concept ${index}`, description: "Synthetic knowledge for large-library verification.",
        path: `concepts/${index}.md`, type: "policy", status: "stable", updatedAt: date, layer: "personal",
        filename: `Synthetic document ${index}.pdf`, relativePath: `library/uploads/${index}.pdf`, sizeBytes: 12000, createdAt: date };
    });
    return route.fulfill({ json: { isAdmin: true, counts: { documents: 125, concepts: 1007, review: 2 }, total,
      page: current, pageSize: 50, searchMode: q ? "hybrid" : "browse", types: ["policy"], documents: documents ? rows : [], concepts: documents ? [] : rows } });
  });
  await page.route("**/api/library/concepts/*", async route => {
    if (route.request().method() === "PATCH") {
      if (conflict) return route.fulfill({ status: 409, json: { error: "This concept changed while you were editing. Copy your changes, then reload the latest version." } });
      patches++;
      savedTitle = route.request().postDataJSON().title;
      return route.fulfill({ json: { status: "saved", searchIndexed: true } });
    }
    return route.fulfill({ json: { concept: { id: id(0), userId: "owner", title: savedTitle, description: "Synthetic knowledge for large-library verification.",
      type: "policy", body: "## Restock\nOrder more before inventory runs out.", updatedAt: date, status: "stable", sources: [], verified: [] } } });
  });
  await page.route("**/api/library/packs", route => route.fulfill({ json: { packs: [] } }));
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 844 });
    await page.goto("/library?view=concepts");
    await expect(page.getByRole("status", { name: "" }).filter({ hasText: "1,007 concepts" })).toBeVisible();
    await expect(page.locator(".library-browser-list > li")).toHaveCount(50);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 390) {
      await expect(page.getByRole("combobox", { name: "Visibility", exact: true })).toBeHidden();
      await page.getByRole("button", { name: "Filters", exact: true }).click();
      await expect(page.getByRole("combobox", { name: "Visibility", exact: true })).toBeVisible();
      await page.getByRole("button", { name: "Filters", exact: true }).click();
    }
    await page.screenshot({ path: testInfo.outputPath(`library-list-${width}.png`) });
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await expect(page).toHaveURL(/page=2/);
    await expect(page.getByRole("link", { name: "Synthetic concept 50", exact: true })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole("link", { name: savedTitle, exact: true })).toBeVisible();
    const search = page.getByRole("searchbox", { name: "Search library" });
    await search.fill("avoid stockouts");
    await expect(page).toHaveURL(/q=avoid\+stockouts/);
    await expect(page.locator(".library-browser-list > li")).toHaveCount(1);
    await page.getByRole("link", { name: savedTitle, exact: true }).click();
    const detail = page.getByRole("region", { name: "Library details" });
    await expect(detail.getByRole("heading", { name: savedTitle, exact: true })).toBeVisible();
    expect(await detail.evaluate(element => {
      const rect = element.getBoundingClientRect();
      const rail = document.querySelector(".app-rail-wrap")!;
      const railRight = getComputedStyle(rail).display === "none" ? 0 : rail.getBoundingClientRect().right;
      return rect.left >= railRight && rect.right <= innerWidth;
    })).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`library-detail-${width}.png`) });
    await page.getByRole("button", { name: "Edit concept", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "Edit concept", exact: true });
    await expect(editor).toBeInViewport();
    expect(await editor.evaluate(element => getComputedStyle(element).position)).toBe("fixed");
    await expect(editor.getByRole("combobox", { name: "Category", exact: true })).toHaveValue("policy");
    await editor.getByRole("combobox", { name: "Category", exact: true }).selectOption("Playbook");
    await editor.getByRole("textbox", { name: "Title", exact: true }).fill(`Updated concept ${width}`);
    await page.screenshot({ path: testInfo.outputPath(`library-editor-${width}.png`) });
    await editor.getByRole("button", { name: "Cancel", exact: true }).click();
    const discard = page.getByRole("alertdialog");
    await expect(discard).toBeInViewport();
    await discard.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(editor.getByRole("textbox", { name: "Title", exact: true })).toHaveValue(`Updated concept ${width}`);
    conflict = true;
    await editor.getByRole("button", { name: "Save concept" }).click();
    await expect(editor.getByRole("alert")).toContainText("changed while you were editing");
    conflict = false;
    await editor.getByRole("button", { name: "Save concept" }).click();
    await expect(editor).toHaveCount(0);
    await expect(detail.getByRole("heading", { name: `Updated concept ${width}`, exact: true })).toBeVisible();
    await detail.getByRole("button", { name: "Back to list" }).click();
    await search.fill("no-match");
    await expect(page.getByRole("heading", { name: "No matches", exact: true })).toBeVisible();
    await search.fill("server-error");
    await expect(page.locator(".library-error[role=alert]")).toContainText("Library could not be loaded");
  }
  expect(patches).toBe(2);
});
