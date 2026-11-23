import { describe, expect, it } from "vitest";
import { parseSourceChangeFilter } from "../src/workflows/subscription-query";

describe("parseSourceChangeFilter", () => {
  it("returns the filter for a valid shape", () => {
    const parsed = parseSourceChangeFilter({
      table: "productinventory",
      where: { quantity: { lt: 100 } },
      primary_key: ["productid", "locationid"],
      version_column: "modifieddate",
    });
    expect(parsed).not.toBeNull();
    expect(parsed!.table).toBe("productinventory");
    expect(parsed!.primary_key).toEqual(["productid", "locationid"]);
  });

  it("rejects an empty primary_key array", () => {
    expect(
      parseSourceChangeFilter({
        table: "productinventory",
        primary_key: [],
      }),
    ).toBeNull();
  });
});
