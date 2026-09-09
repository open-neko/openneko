import { expect, test } from "@playwright/test";

test("connects and disconnects the sandbox test pack through the browser", async ({ page }, info) => {
  test.setTimeout(180_000);
  // The fixture provider has no real accounts or tokens. Only the provider's
  // consent page is simulated; web routes, worker, DB and OpenShell are live.
  let number = 0;
  await page.route("https://provider.example/authorize?**", async route => {
    const url = new URL(route.request().url());
    const callback = new URL(url.searchParams.get("redirect_uri")!);
    callback.searchParams.set("state", url.searchParams.get("state")!);
    callback.searchParams.set("code", `${url.searchParams.get("code_challenge")}:account${++number}`);
    await route.fulfill({ contentType: "text/html", body: `<a href="${callback.toString().replaceAll("&", "&amp;")}">Authorize fixture account</a>` });
  });
  await expect.poll(async () => (await page.request.post("http://127.0.0.1:4113/admin/pack-accounts", { data: { owner: "solo" } })).ok(), { timeout: 30000 }).toBe(true);
  await page.goto("/integrations/packs");
  const section = page.getByRole("region", { name: "Test pack account" });
  await expect(section).toBeVisible();
  const replacing = !await section.getByText("An administrator must save the OAuth client settings before you connect.", { exact: true }).isVisible();
  await section.getByText("OAuth client settings", { exact: true }).click();
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(section.getByLabel("Client secret", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await section.screenshot({ path: info.outputPath(`pack-client-${width}.png`) });
  }
  await section.getByLabel("Client ID", { exact: true }).fill(`fixture-${Date.now()}`);
  await section.getByLabel("Client secret", { exact: true }).fill("fixture-only-secret");
  await section.getByRole("button", { name: "Save client settings", exact: true }).click();
  const dialog = page.getByRole("dialog");
  if (replacing) await dialog.getByRole("button", { name: "Replace settings", exact: true }).click();
  await expect(section.getByRole("button", { name: "Connect an account", exact: true })).toBeEnabled();
  for (let i = 0; i < 2; i++) {
    await section.getByRole("button", { name: "Connect an account", exact: true }).click();
    await expect(section.getByRole("button", { name: "Please wait…", exact: true })).toBeDisabled();
    await page.getByRole("link", { name: "Authorize fixture account" }).click();
    await expect(page).toHaveURL(/connected=1/);
    await expect(section.getByLabel("Account", { exact: true })).toBeVisible();
  }
  await section.getByLabel("Account", { exact: true }).selectOption({ label: "account1@example.test" });
  const accountId = await section.getByLabel("Account", { exact: true }).inputValue();
  const execution = await page.request.get(`http://127.0.0.1:4113/fixture/read/${accountId}`);
  expect(execution.ok()).toBe(true);
  expect(await execution.json()).toMatchObject({ accountId: "account1", value: "browser" });
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const control = section.getByRole("button", { name: "Reconnect", exact: true });
    await control.focus();
    expect(await control.evaluate(el => { const style = getComputedStyle(el); return el === document.activeElement && (style.outlineStyle !== "none" || style.boxShadow !== "none"); })).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    if (width === 390) expect((await control.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await section.screenshot({ path: info.outputPath(`pack-accounts-${width}.png`) });
  }
  await section.getByRole("button", { name: "Disconnect", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Disconnect", exact: true }).click();
  await expect(section.getByLabel("Account", { exact: true }).locator("option")).toHaveCount(2);
  await page.reload();
  await expect(section.getByLabel("Account", { exact: true }).locator("option")).toHaveCount(2);
});


test("shows loading, error, retry and empty account states", async ({ page }, info) => {
  let fail = true;
  await page.route("**/api/pack-accounts", async route => {
    await new Promise(resolve => setTimeout(resolve, 300));
    return route.fulfill({ status: fail ? 503 : 200, json: fail ? { error: "Unavailable" } : { providers: [], canConfigure: false } });
  });
  await page.goto("/integrations/packs");
  await expect(page.getByRole("status", { name: "" }).filter({ hasText: "Loading pack accounts" })).toBeVisible();
  await expect(page.getByRole("alert").filter({ hasText: "Pack accounts could not be loaded" })).toBeFocused();
  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: info.outputPath("pack-accounts-error.png") });
  fail = false;
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(page.getByText("No installed packs require an account.", { exact: true })).toBeVisible();
});
