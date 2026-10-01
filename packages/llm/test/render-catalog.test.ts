import { describe, expect, it } from "vitest";
import {
  RENDER_CARDS_DESCRIPTION,
  RENDER_CARDS_INPUT_SCHEMA,
} from "../src/work/render-catalog";

describe("render_cards catalog", () => {
  const messagesDescription = (
    RENDER_CARDS_INPUT_SCHEMA.properties as Record<string, { description: string }>
  ).messages.description;

  it("keeps discovery text short and searchable", () => {
    expect(RENDER_CARDS_DESCRIPTION).toContain("A2UI v1.0");
    expect(RENDER_CARDS_DESCRIPTION).toContain("{messages:[...]}");
    expect(RENDER_CARDS_DESCRIPTION).toContain("Render cards and interactive UI");
    expect(RENDER_CARDS_DESCRIPTION.length).toBeLessThan(500);
    expect(RENDER_CARDS_DESCRIPTION).not.toContain("Example — editable source proposal:");
  });

  it("gives the model a strict v1 component schema", () => {
    const schema = JSON.stringify(RENDER_CARDS_INPUT_SCHEMA);
    expect(schema).toContain('"const":"v1.0"');
    expect(schema).toContain('"required":["id","component"]');
    expect(schema).toContain("urn:openneko:catalog:work:v2");
    expect(messagesDescription).toContain("urn:openneko:catalog:work:v2");
    expect(messagesDescription).toContain("TextField");
    expect(messagesDescription).toContain("ChoicePicker");
    expect(messagesDescription).toContain("Conditional");
    expect(messagesDescription).toContain("Button");
    expect(messagesDescription).toContain("OpenApiSpecInput");
    expect(messagesDescription).toContain("ManagedFileSourceInput");
    expect(messagesDescription).toContain("Chart: standalone evidence chart");
    expect(messagesDescription).toContain("Choose Chart only when");
    expect(messagesDescription).toContain("Example — editable source proposal:");
    expect(messagesDescription).toContain('"values":{"path":"/form"}');
    expect(messagesDescription).toContain('"label":"Files","value":"file"');
    expect(messagesDescription).toContain("/form/openApiSpec/id");
    expect(messagesDescription).toContain("Storage backend");
    expect(messagesDescription).toContain("/form/localFiles/sourceName");
  });

  it("keeps configuration submission on the proposal path", () => {
    expect(messagesDescription).toContain("proposal tool");
    expect(messagesDescription).toContain("approval policy");
    expect(messagesDescription).toContain("secretRef");
  });
});
