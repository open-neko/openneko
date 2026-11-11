import { describe, expect, it } from "vitest";
import { createElement, type ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { applyMessage } from "@/a2ui/surface";
import { renderComponent } from "@/a2ui/renderer";
import { WORK_CATALOG_ID } from "@/a2ui/catalog";
import { blockedTablesText, readSourceCheck, SourceCheckFindings, splitTableList } from "@/a2ui/source-check";
import "@/a2ui/components";

function html(form: Record<string, unknown>) {
  const surface = applyMessage(new Map(), {
    version: "v1.0",
    createSurface: {
      surfaceId: "source-form",
      catalogId: WORK_CATALOG_ID,
      dataModel: { form },
      components: [
        { id: "root", component: "Column", children: ["check"] },
        {
          id: "check",
          component: "SourceConnectionCheck",
          type: { path: "/form/type" },
          host: { path: "/form/host" },
          port: { path: "/form/port" },
          dbname: { path: "/form/dbname" },
          user: { path: "/form/user" },
          secretRef: { path: "/form/secretRef" },
          value: { path: "/form/blockedTables" },
        },
      ],
    },
  }).get("source-form")!;
  return renderToStaticMarkup(
    renderComponent(surface.components.get("root")!, { surface, extras: {} }) as ReactElement,
  );
}

describe("SourceConnectionCheck card", () => {
  it("asks for the answers and the saved password before it can run", () => {
    const out = html({ type: ["postgres"], host: "db", port: 5432, dbname: "", user: "", secretRef: "" });
    expect(out).toContain("Check connection");
    expect(out).toContain("disabled=\"\"");
    expect(out).toContain("save the password first");
  });

  it("can run when every answer is there", () => {
    const out = html({ type: ["mysql"], host: "db", port: 3306, dbname: "shop", user: "r", secretRef: "shop-password" });
    expect(out).not.toContain("save the password first");
    expect(out).not.toContain("disabled=\"\"");
  });

  it("turns the cleared tables into the blocked list and keeps earlier blocked tables", () => {
    const tables = ["orders", "customers", "admin_user"];
    expect(blockedTablesText(tables, new Set(tables))).toBe("");
    expect(blockedTablesText(tables, new Set(["orders"]))).toBe("customers, admin_user");
    expect(blockedTablesText(tables, new Set(tables), ["oauth_token", "orders"])).toBe("oauth_token");
    expect(splitTableList(" a, ,b ,")).toEqual(["a", "b"]);
  });

  it("reads a check result and shows each step with its SQL", () => {
    const check = readSourceCheck({
      check: {
        ok: false,
        supported: true,
        findings: [
          { step: "login", status: "ok", message: "r signs in." },
          { step: "read_only", status: "fail", message: "r can change data.", sql: ["REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'r'@'%';"] },
        ],
        tables: [{ name: "orders", rows: 3 }],
        moreTables: 0,
      },
    });
    expect(check?.findings).toHaveLength(2);
    expect(readSourceCheck({ error: "admin only" })).toBeNull();
    const out = renderToStaticMarkup(createElement(SourceCheckFindings, { check: check! }));
    expect(out).toContain("Passed");
    expect(out).toContain("Failed");
    expect(out).toContain("SQL for the database administrator");
    expect(out).toContain("REVOKE ALL PRIVILEGES, GRANT OPTION FROM &#x27;r&#x27;@&#x27;%&#x27;;");
  });
});
