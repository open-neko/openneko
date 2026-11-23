import { describe, expect, it } from "vitest";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import "@/a2ui/components";
import { WORK_CATALOG_ID } from "@/a2ui/catalog";

function html(problems: Record<string, string>) {
  const s = applyMessage(new Map(), {
    version: "v1.0",
    createSurface: {
      surfaceId: "source-form",
      catalogId: WORK_CATALOG_ID,
      dataModel: { form: { name: "shop", user: "", type: ["mysql"], secretRef: "" }, problems },
      components: [
        { id: "root", component: "Column", children: ["problemsNote", "user", "type", "password"] },
        { id: "problemsNote", component: "Conditional", when: { path: "/problems/general" }, children: ["problemsCallout"] },
        { id: "problemsCallout", component: "Callout", title: "Fix these answers", text: { path: "/problems/general" } },
        { id: "user", component: "TextField", label: "Read-only login", value: { path: "/form/user" }, error: { path: "/problems/user" } },
        {
          id: "type",
          component: "ChoicePicker",
          label: "Database type",
          options: [{ label: "MySQL", value: "mysql" }],
          value: { path: "/form/type" },
          variant: "mutuallyExclusive",
          error: { path: "/problems/type" },
        },
        {
          id: "password",
          component: "SourceSecretInput",
          label: "Password",
          sourceName: { path: "/form/name" },
          value: { path: "/form/secretRef" },
          error: { path: "/problems/secretRef" },
        },
      ],
    },
  }).get("source-form")!;
  return renderToStaticMarkup(
    renderComponent(s.components.get("root")!, { surface: s, extras: {} }) as ReactElement,
  );
}

describe("field errors on the source form", () => {
  it("shows each problem beside its answer", () => {
    const out = html({
      user: "The login name is empty.",
      type: "GraphJin does not support this database type.",
      secretRef: "Save the password for this login first.",
      general: "GraphJin did not accept this change.",
    });
    expect(out).toContain('aria-invalid="true"');
    expect(out).toContain('aria-describedby="source-form-user-error"');
    expect(out).toMatch(/id="source-form-user-error"[^>]*role="alert"[^>]*>The login name is empty\./);
    expect(out).toContain("GraphJin does not support this database type.");
    expect(out).toContain("Save the password for this login first.");
    expect(out).toContain("Fix these answers");
    expect(out).toContain("GraphJin did not accept this change.");
  });

  it("shows nothing when the answers have no problems", () => {
    const out = html({});
    expect(out).not.toContain("work-a2ui-field-error");
    expect(out).not.toContain('aria-invalid="true"');
    expect(out).not.toContain("Fix these answers");
  });
});
