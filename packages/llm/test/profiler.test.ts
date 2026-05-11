import { describe, expect, it } from "vitest";
import { validateBusinessProfile } from "../src/profiler";

const VALID_PROFILE = `# AdventureWorks Cycles — Business Profile

## What they are
AdventureWorks Cycles designs and sells bicycles and accessories.

## Who they serve
Specialty retailers and direct consumers.

## Where they operate
North America, Europe, and Australia.

## Scale
- Date range covered, recent volume + value
- Top revenue drivers
- People served and people who do the work

## Operational footprint
Manufacturing, sales, fulfillment, and finance.

## What a downstream LLM should hold in mind
- Bikes and accessories are the core offer.
- Wholesale and DTC both matter.
- Europe is a growth priority.`;

describe("validateBusinessProfile", () => {
  it("accepts a profile that matches the required markdown contract", () => {
    expect(validateBusinessProfile(VALID_PROFILE, "AdventureWorks Cycles")).toBe(
      VALID_PROFILE,
    );
  });

  it("rejects agent failure/apology text", () => {
    const failure = `# AdventureWorks Cycles — Business Profile

## What they are
I am sorry, but I was unable to connect to the GraphJin server.

## Who they serve
Not measured.

## Where they operate
Not measured.

## Scale
Not measured.

## Operational footprint
Not measured.

## What a downstream LLM should hold in mind
Not measured.`;

    expect(() =>
      validateBusinessProfile(failure, "AdventureWorks Cycles"),
    ).toThrow(/failure text/);
  });

  it("rejects output that does not start with the exact profile heading", () => {
    expect(() =>
      validateBusinessProfile("Here is the profile:\n\n" + VALID_PROFILE, "AdventureWorks Cycles"),
    ).toThrow(/expected heading/);
  });
});
