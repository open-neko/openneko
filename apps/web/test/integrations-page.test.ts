import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, expect, it, vi } from "vitest";
import type { PackUserConnectionStatus } from "@neko/llm/graphjin/pack-user-connections";

const mocks = vi.hoisted(() => ({
  state: null as string | null,
  loaded: { role: "member" as const, workspace: [], connectors: [], personal: [] as PackUserConnectionStatus[] },
  fixture: vi.fn((): PackUserConnectionStatus[] => []),
  rendered: null as { initial: { personal?: PackUserConnectionStatus[] }; preview?: boolean } | null,
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => ({
    get: (key: string) => (key === "state" ? mocks.state : null),
  }),
}));
vi.mock("../src/app/integrations/load", () => ({
  useIntegrationsData: () => mocks.loaded,
}));
vi.mock("../src/app/integrations/visual-fixture", () => ({
  personalConnectionsFixture: mocks.fixture,
  pluginConnectionsFixture: { workspace: [], connectors: [] },
}));
vi.mock("../src/app/integrations/IntegrationsList", () => ({
  default: (props: { initial: { personal?: PackUserConnectionStatus[] }; preview?: boolean }) => {
    mocks.rendered = props;
    return null;
  },
}));

import { IntegrationsView } from "../src/app/integrations/page";

afterEach(() => {
  mocks.state = null;
  mocks.rendered = null;
  vi.unstubAllEnvs();
  vi.clearAllMocks();
});

it("uses installed account state on the normal route even when visual testing is enabled", () => {
  vi.stubEnv("OPENNEKO_VISUAL_TEST", "true");
  renderToStaticMarkup(createElement(IntegrationsView));
  expect(mocks.fixture).not.toHaveBeenCalled();
  expect(mocks.rendered?.preview).toBeUndefined();
  expect(mocks.rendered?.initial.personal).toEqual([]);
});

it("requires an explicit connection state to render visual fixtures", () => {
  vi.stubEnv("OPENNEKO_VISUAL_TEST", "true");
  mocks.state = "connected";
  renderToStaticMarkup(createElement(IntegrationsView));
  expect(mocks.fixture).toHaveBeenCalledWith("connected");
  expect(mocks.rendered?.preview).toBe(true);
});
