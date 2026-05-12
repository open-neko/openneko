import { describe, expect, it } from "vitest";
import { extractSurfaceMessages } from "../src/agent-backends/hermes";

describe("extractSurfaceMessages", () => {
  it("returns text and empty messages when no fence is present", () => {
    const result = extractSurfaceMessages("Just some prose, no cards.");
    expect(result.text).toBe("Just some prose, no cards.");
    expect(result.messages).toEqual([]);
  });

  it("trims surrounding whitespace when no fence is present", () => {
    const result = extractSurfaceMessages("\n\n  hello  \n\n");
    expect(result.text).toBe("hello");
  });

  it("extracts messages and strips the fence from text", () => {
    const raw = [
      "Here are the KPIs:",
      "```neko_a2ui",
      JSON.stringify([
        {
          version: "v0.9",
          createSurface: { surfaceId: "kpis", catalogId: "urn:app:catalog:briefing:v1" },
        },
      ]),
      "```",
      "Anything else?",
    ].join("\n");
    const result = extractSurfaceMessages(raw);
    expect(result.messages).toHaveLength(1);
    expect(result.messages[0].version).toBe("v0.9");
    expect(result.text).toContain("Here are the KPIs:");
    expect(result.text).toContain("Anything else?");
    expect(result.text).not.toContain("neko_a2ui");
    expect(result.text).not.toContain("v0.9");
  });

  it("wraps malformed fence body as a synthetic Markdown surface", () => {
    const raw = "Prose. ```neko_a2ui\n<Markdown>hello there</Markdown>\n``` more prose.";
    const result = extractSurfaceMessages(raw);
    expect(result.text).toContain("Prose.");
    expect(result.text).toContain("more prose.");
    expect(result.messages).toHaveLength(2);
    const components = (result.messages[1] as unknown as {
      updateComponents: { components: Array<{ component: string; text?: string }> };
    }).updateComponents.components;
    expect(components[0].component).toBe("Markdown");
    expect(components[0].text).toBe("hello there");
  });

  it("wraps non-array JSON as a synthetic Markdown surface (preserves the body)", () => {
    const raw = '```neko_a2ui\n{"version": "v0.9"}\n```';
    const result = extractSurfaceMessages(raw);
    expect(result.messages).toHaveLength(2);
    const components = (result.messages[1] as unknown as {
      updateComponents: { components: Array<{ component: string; text?: string }> };
    }).updateComponents.components;
    expect(components[0].component).toBe("Markdown");
    expect(components[0].text).toContain("v0.9");
  });

  it("returns empty messages when fence body is empty after JSX strip", () => {
    const raw = "```neko_a2ui\n<Markdown></Markdown>\n```";
    const result = extractSurfaceMessages(raw);
    expect(result.messages).toEqual([]);
  });

  it("matches the fence regardless of casing", () => {
    const raw =
      '```NEKO_A2UI\n[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"urn:app:catalog:briefing:v1"}}]\n```';
    const result = extractSurfaceMessages(raw);
    expect(result.messages).toHaveLength(1);
  });
});
