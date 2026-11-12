import { describe, expect, it } from "vitest";
import { detectMutationLoop } from "../src/workflows/cycle-detection";
import type { SourceChangeFilter } from "../src/workflows/subscription-query";

describe("detectMutationLoop", () => {
  const filter: SourceChangeFilter = {
    table: "productinventory",
    where: {},
    primary_key: ["productid", "locationid"],
  };

  it("returns loops:false when the workflow text doesn't mention the table", () => {
    const result = detectMutationLoop({
      filter,
      workflow: {
        goal: "send a slack message",
        description: "alerts to ops",
        steps: [{ id: "s1", description: "post to channel" }],
      },
    });
    expect(result.loops).toBe(false);
  });

  it("returns loops:false when the table is mentioned but no mutation verb", () => {
    const result = detectMutationLoop({
      filter,
      workflow: {
        goal: "report productinventory levels",
        description: "",
        steps: [{ id: "s1", description: "read inventory" }],
      },
    });
    expect(result.loops).toBe(false);
  });

  it("flags when the table + a mutation verb both appear", () => {
    const result = detectMutationLoop({
      filter,
      workflow: {
        goal: "draft purchase order and update productinventory quantity",
        description: "",
        steps: [{ id: "s1", description: "modify stock" }],
      },
    });
    expect(result.loops).toBe(true);
    if (result.loops) {
      expect(result.mutationKeyword).toBe("update");
      expect(result.reason).toMatch(/productinventory/);
    }
  });

  it("matches across goal/description/steps corpus (case-insensitive)", () => {
    const result = detectMutationLoop({
      filter,
      workflow: {
        goal: "alert on low stock",
        description: "",
        steps: [
          { id: "s1", description: "Insert a row into ProductInventory" },
        ],
      },
    });
    expect(result.loops).toBe(true);
    if (result.loops) {
      expect(result.mutationKeyword).toBe("insert");
    }
  });
});
