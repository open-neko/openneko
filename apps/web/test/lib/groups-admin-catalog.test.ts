import { describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("@/lib/auth", () => ({ getPluginActionDescriptors: vi.fn(), getPluginStatus: vi.fn() }));
vi.mock("@/lib/integrations", () => ({ listConnectProviders: vi.fn() }));
vi.mock("@/lib/work-files", () => ({ listWorkSkills: vi.fn() }));

const { catalogWhere } = await import("@/lib/groups-admin");

describe("catalogWhere", () => {
  it("sends one condition without an and", () => {
    expect(catalogWhere("database")).toBe('{ kind: { eq: "database" } }');
  });

  it("joins the kind and source conditions with and", () => {
    expect(catalogWhere("column", "adventureworks")).toBe('{ and: [{ kind: { eq: "column" } }, { database_name: { eq: "adventureworks" } }] }');
  });
});
