import { describe, expect, it } from "vitest";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import { sourceSecretName } from "@/a2ui/components";
import { WORK_CATALOG_ID } from "@/a2ui/catalog";

function surface(secretRef: string) {
  return applyMessage(new Map(), {
    version: "v1.0",
    createSurface: {
      surfaceId: "source-form",
      catalogId: WORK_CATALOG_ID,
      dataModel: { form: { name: "shop db", secretRef } },
      components: [
        { id: "root", component: "Column", children: ["password"] },
        {
          id: "password",
          component: "SourceSecretInput",
          label: "Password",
          sourceName: { path: "/form/name" },
          value: { path: "/form/secretRef" },
        },
      ],
    },
  }).get("source-form")!;
}

const html = (secretRef: string) => {
  const s = surface(secretRef);
  return renderToStaticMarkup(
    renderComponent(s.components.get("root")!, { surface: s, extras: {} }) as ReactElement,
  );
};

describe("SourceSecretInput", () => {
  it("renders a password field that starts empty", () => {
    const out = html("");
    expect(out).toContain('type="password"');
    expect(out).toContain("Save password");
    expect(out).toContain("Chat sees its name only.");
  });

  it("shows the stored name, never a value, once saved", () => {
    const out = html("shop-db-password");
    expect(out).toContain("Replace password");
    expect(out).toContain("Saved as &quot;shop-db-password&quot;");
  });

  it("names the secret after the source with only allowed characters", () => {
    expect(sourceSecretName("shop db")).toBe("shop-db-password");
    expect(sourceSecretName("  ")).toBe("");
    expect(sourceSecretName("a/b:c")).toMatch(/^[A-Za-z0-9._-]+$/);
  });
});
