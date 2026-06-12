import { describe, expect, it } from "vitest";
import { buildProfilerPrompt, validateBusinessProfile } from "../src/profiler";

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

describe("buildProfilerPrompt knowledge inlining", () => {
  const bigTables = JSON.stringify({
    tables: Array.from({ length: 400 }, (_, i) => ({
      name: `table_${i}`,
      schema: "public",
      summary: `summary for table ${i} `.repeat(4),
    })),
  });
  const bigInsights = JSON.stringify({
    hub_tables: Array.from({ length: 10 }, (_, i) => ({
      name: `hub_${i}`,
      summary: "x".repeat(300),
      examples: [`{ hub_${i}(limit: 10) { id } }`],
      join_paths: Array.from(
        { length: 8 },
        (_, j) => `public.hub_${i}.col_${j} -> public.other_${j}.id`,
      ),
    })),
    help_cards: Array.from({ length: 30 }, (_, i) => ({
      id: `help:topic${i}`,
      summary: "y".repeat(200),
    })),
  });

  it("agentic mode inlines capped digests, never the raw pack files", () => {
    const prompt = buildProfilerPrompt({
      orgName: "Acme",
      companyNote: "",
      shellTool: "terminal",
      knowledge: {
        mode: "agentic",
        tables: bigTables,
        namespaces: "{}",
        insights: bigInsights,
        syntax: '{"essentials":[],"patterns":[]}',
      },
    });
    expect(prompt).not.toContain(bigTables);
    expect(prompt).not.toContain(bigInsights);
    expect(prompt).toContain("join: public.hub_0.col_0 -> public.other_0.id");
    expect(prompt).toContain("- help:topic0");
    // Raw agentic packs run 50KB+; the whole prompt must stay well under
    // the size that hangs the hermes first stream.
    expect(prompt.length).toBeLessThan(25_000);
  });

  it("legacy mode keeps the full prefetched pack inline", () => {
    const prompt = buildProfilerPrompt({
      orgName: "Acme",
      companyNote: "",
      shellTool: "terminal",
      knowledge: {
        mode: "legacy",
        tables: bigTables,
        namespaces: "{}",
        insights: '{"hub_tables":[]}',
        syntax: "{}",
      },
    });
    expect(prompt).toContain(bigTables);
  });
});
