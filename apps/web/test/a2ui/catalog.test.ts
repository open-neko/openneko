import { describe, expect, it } from "vitest";
import { CATALOG_ID, ComponentTypes } from "@/a2ui/catalog";

describe("A2UI catalog", () => {
  it("CATALOG_ID is a non-empty URN", () => {
    expect(CATALOG_ID.length).toBeGreaterThan(0);
    expect(CATALOG_ID).toMatch(/^urn:/);
  });

  it("ComponentTypes keys equal their string values (no typos)", () => {
    for (const [key, value] of Object.entries(ComponentTypes)) {
      expect(value).toBe(key);
    }
  });

  it("includes the four core component types the briefing flow uses", () => {
    expect(ComponentTypes).toMatchObject({
      Briefing: "Briefing",
      BriefingCard: "BriefingCard",
      MetricCard: "MetricCard",
      ChatResponse: "ChatResponse",
    });
  });
});
