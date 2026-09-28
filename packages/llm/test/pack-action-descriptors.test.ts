import { describe, expect, it, vi } from "vitest";

const fixture = vi.hoisted(() => ({ rows: [] as Array<{ definition: unknown }> }));
vi.mock("@neko/db", () => ({
  db: () => ({ select: () => ({ from: () => ({ where: async () => fixture.rows }) }) }),
  and: (...values: unknown[]) => values,
  eq: (...values: unknown[]) => values,
  pack_action_definition: { definition: {}, org_id: {}, enabled: {}, readiness: {} },
}));
vi.mock("../src/workflows/action-executor", () => ({
  getRegisteredPackActionKinds: () => ["native.action"],
}));

import { listPackActionDescriptors } from "../src/work/pack-action-descriptors";

describe("Harness pack action discovery", () => {
  it("does not advertise ready actions without an executable adapter", async () => {
    fixture.rows = [
      { definition: { kind: "missing.action", description: "No worker adapter" } },
      { definition: { kind: "native.action", description: "Registered worker adapter" } },
      { definition: { kind: "api.action", description: "GraphJin API action", adapter: {
        kind: "graphjin_api_operation", operations: { update: { mutationRoot: "update_api" } },
      } } },
      { definition: { kind: "broken.action", description: "No operation", adapter: {
        kind: "graphjin_api_operation", operations: {},
      } } },
    ];
    expect((await listPackActionDescriptors("org", { forHarness: true })).map(action => action.kind))
      .toEqual(["native.action", "api.action"]);
    expect((await listPackActionDescriptors("org")).map(action => action.kind))
      .toEqual(["missing.action", "native.action", "api.action", "broken.action"]);
  });
});
