import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const PLANTED = "Planted-Pw-41c2e8";
const mocks = vi.hoisted(() => ({
  actor: { userId: "u1", role: "member" as string },
  read: vi.fn(),
}));

vi.mock("next/server", () => ({ connection: async () => {} }));
vi.mock("@/lib/actor", () => ({ getCurrentActor: async () => mocks.actor }));
vi.mock("@/lib/db", () => ({ getOrgId: async () => "org" }));
vi.mock("@neko/db", () => ({ db: vi.fn(), asc: vi.fn(), desc: vi.fn(), eq: vi.fn(), data_source: {}, data_source_secret: {} }));
vi.mock("@/lib/graphjin-config-settings", () => ({ getGraphjinConfigSettingsPayload: vi.fn() }));
vi.mock("@neko/llm/graphjin/config-file-view", async (load) => {
  const real = await load<typeof import("@neko/llm/graphjin/config-file-view")>();
  mocks.read.mockImplementation(real.readGraphjinConfigFileView);
  return { readGraphjinConfigFileView: mocks.read };
});

import AdminGraphjinPage from "../src/app/admin/settings/graphjin/page";
import { ConfigFileView } from "../src/app/admin/settings/graphjin/ConfigFileView";

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), "gj-web-view-"));
});
afterEach(async () => {
  vi.unstubAllEnvs();
  mocks.read.mockClear();
  await rm(dir, { recursive: true, force: true });
});

it("gives a non-admin nothing from the config file", async () => {
  mocks.actor.role = "member";
  const html = renderToStaticMarkup(await AdminGraphjinPage());
  expect(html).not.toContain("Config file");
  expect(mocks.read).not.toHaveBeenCalled();
});

it("shows the admin the path, the state and the YAML with no planted password", async () => {
  const file = join(dir, "agentic.yml");
  await writeFile(
    file,
    `databases:\n  shop:\n    host: db.internal\n    password: ${PLANTED}\n    connection_string: postgres://u:${PLANTED}@db.internal/shop\n`,
  );
  await writeFile(
    join(dir, ".openneko-graphjin-state"),
    JSON.stringify({ state: "running", since: "2026-11-11T08:00:00Z", restarts: 0 }),
  );
  vi.stubEnv("OPENNEKO_GRAPHJIN_CONFIG", file);

  const html = renderToStaticMarkup(createElement("div", null, await ConfigFileView()));

  expect(html).toContain(file);
  expect(html).toContain("GraphJin is running.");
  expect(html).toContain("openneko graphjin check");
  expect(html).toContain("host: db.internal");
  expect(html).not.toContain(PLANTED);
  expect(html).not.toMatch(/<(input|textarea|form)\b/);
});
