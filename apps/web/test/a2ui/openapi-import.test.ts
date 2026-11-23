import { describe, expect, it } from "vitest";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import { WORK_CATALOG_ID } from "@/a2ui/catalog";
import "@/a2ui/components";

describe("OpenApiSpecInput from the import tool", () => {
  it("shows the exact URL and the host that OpenNeko fetches from, before any fetch", () => {
    const s = applyMessage(new Map(), {
      version: "v1.0",
      createSurface: {
        surfaceId: "openapi-import-1",
        catalogId: WORK_CATALOG_ID,
        dataModel: {},
        components: [
          { id: "root", component: "Column", children: ["spec"] },
          {
            id: "spec",
            component: "OpenApiSpecInput",
            label: "OpenAPI specification",
            url: "https://attacker.example/pets.yaml",
            baseUrl: "https://api.example.com/v2",
            value: { path: "/openApiSpec" },
          },
        ],
      },
    }).get("openapi-import-1")!;
    const out = renderToStaticMarkup(renderComponent(s.components.get("root")!, { surface: s, extras: {} }) as ReactElement);
    expect(out).toContain('value="https://attacker.example/pets.yaml"');
    expect(out).toContain("OpenNeko fetches this file from attacker.example when you press Import.");
    expect(out).toContain('value="https://api.example.com/v2"');
    expect(out).toContain("Import and validate");
  });
});
