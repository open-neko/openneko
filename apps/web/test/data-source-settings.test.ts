import { describe, expect, it } from "vitest";
import { validateDraft } from "@/lib/data-source-settings";

describe("validateDraft — data source URL validation", () => {
  it("accepts a fully-populated http draft", () => {
    expect(
      validateDraft({
        graphqlUrl: "http://localhost:8080/api/v1/graphql",
        mcpUrl: "http://localhost:8080/api/v1/mcp",
        label: "primary",
      }),
    ).toEqual([]);
  });

  it("accepts https URLs", () => {
    expect(
      validateDraft({
        graphqlUrl: "https://api.example.com/graphql",
        mcpUrl: "https://api.example.com/mcp",
      }),
    ).toEqual([]);
  });

  it("requires graphqlUrl", () => {
    const errs = validateDraft({ graphqlUrl: "" });
    expect(errs).toContain("GraphQL URL is required.");
  });

  it("trims whitespace before checking presence", () => {
    const errs = validateDraft({ graphqlUrl: "   " });
    expect(errs).toContain("GraphQL URL is required.");
  });

  it("rejects non-URL graphql values", () => {
    const errs = validateDraft({ graphqlUrl: "not a url" });
    expect(errs.some((e) => /not a valid URL/.test(e))).toBe(true);
  });

  it("rejects unsupported protocols (ftp, file, javascript)", () => {
    for (const url of [
      "ftp://example.com/graphql",
      "file:///etc/passwd",
      "javascript:alert(1)",
    ]) {
      const errs = validateDraft({ graphqlUrl: url });
      expect(
        errs.some(
          (e) => /must use http or https/.test(e) || /not a valid URL/.test(e),
        ),
        `expected protocol/url error for ${url}, got ${JSON.stringify(errs)}`,
      ).toBe(true);
    }
  });

  it("mcpUrl is optional — empty/missing is fine", () => {
    expect(
      validateDraft({ graphqlUrl: "https://x/graphql" }),
    ).toEqual([]);
    expect(
      validateDraft({ graphqlUrl: "https://x/graphql", mcpUrl: "" }),
    ).toEqual([]);
    expect(
      validateDraft({ graphqlUrl: "https://x/graphql", mcpUrl: null }),
    ).toEqual([]);
  });

  it("validates mcpUrl protocol when present", () => {
    const errs = validateDraft({
      graphqlUrl: "https://x/graphql",
      mcpUrl: "ftp://x/mcp",
    });
    expect(errs.some((e) => /MCP URL must use http or https/.test(e))).toBe(true);
  });

  it("collects multiple errors in one pass", () => {
    const errs = validateDraft({
      graphqlUrl: "",
      mcpUrl: "not-a-url",
    });
    expect(errs.length).toBeGreaterThanOrEqual(2);
  });
});
