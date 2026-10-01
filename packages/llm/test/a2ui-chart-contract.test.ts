import { describe, expect, it } from "vitest";
import { validateRenderCardsInput } from "../src/work/a2ui-contract";

function chartInput(chart: Record<string, unknown>) {
  return {
    messages: [{
      version: "v1.0",
      createSurface: {
        surfaceId: "orders-chart",
        catalogId: "urn:openneko:catalog:work:v2",
        dataModel: {},
        components: [
          { id: "root", component: "Answer", title: "Orders", children: ["chart"] },
          { id: "chart", component: "Chart", title: "Orders by week", type: "line", valueLabel: "Orders", data: [
            { d: "Sep 7", v: 42 }, { d: "Sep 14", v: 47 },
          ], ...chart },
        ],
      },
    }],
  };
}

describe("agent chart contract", () => {
  it("accepts a standalone chart in an Answer", () => {
    expect(validateRenderCardsInput(chartInput({})).success).toBe(true);
    expect(validateRenderCardsInput(chartInput({ data: { path: "/series" } })).success).toBe(true);
  });

  it("rejects malformed points and unsupported chart types before rendering", () => {
    const invalidPoints = validateRenderCardsInput(chartInput({ data: [{ d: "Sep 7", v: "42" }] }));
    expect(invalidPoints.success).toBe(false);
    if (!invalidPoints.success) expect(invalidPoints.issues.some((issue) => issue.code === "invalid_chart_data")).toBe(true);

    const invalidType = validateRenderCardsInput(chartInput({ type: "kpi" }));
    expect(invalidType.success).toBe(false);
    if (!invalidType.success) expect(invalidType.issues.some((issue) => issue.code === "invalid_chart_properties")).toBe(true);
  });

  it("rejects a donut whose categories cannot form a whole", () => {
    const result = validateRenderCardsInput(chartInput({ type: "donut", data: [
      { d: "A", v: 0 }, { d: "B", v: 0 },
    ] }));
    expect(result.success).toBe(false);
    if (!result.success) expect(result.issues.some((issue) => issue.code === "invalid_donut_data")).toBe(true);
  });
});
