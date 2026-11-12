import { describe, expect, it } from "vitest";
import { RECKON_SEED_KEY, reckonSeedMessageFrom } from "../src/workflows/compat";

describe("reckon webhook seed", () => {
  it("reads the start message the Go front stores", () => {
    expect(reckonSeedMessageFrom({ [RECKON_SEED_KEY]: { mode: "batch", message: "Begin." } })).toBe("Begin.");
    expect(reckonSeedMessageFrom({ [RECKON_SEED_KEY]: { message: "  " } })).toBeNull();
    expect(reckonSeedMessageFrom({ other: 1 })).toBeNull();
    expect(reckonSeedMessageFrom(null)).toBeNull();
  });
});
