import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ActionChanges, hasActionChanges } from "@/components/ActionChanges";
import { diffLines } from "@/lib/action-changes";
import { configChangeItems, readConfigChangeSummary } from "@/lib/config-summary";

const payload = {
  action: "set_source_tables",
  source: "shop",
  preview: {
    valid: true,
    summary: {
      before: {
        sources: [{ name: "shop", kind: "database", sentences: ["Admins only (1): sales_order."] }],
        roles: [],
      },
      after: {
        sources: [{ name: "shop", kind: "database", sentences: ["Hidden from everyone (1): customer_entity."] }],
        roles: [{ name: "support", sentence: "A person gets this role when role = 'support'." }],
      },
      yaml: {
        before: "sources:\n  - name: shop\n    admin_tables: [sales_order]\n",
        after: "sources:\n  - name: shop\n    blocked_tables: [customer_entity]\n",
      },
    },
  },
};

describe("config change summary", () => {
  it("reads the before and after of a source_config_admin proposal", () => {
    const summary = readConfigChangeSummary(payload);
    expect(summary).not.toBeNull();
    expect(configChangeItems(summary!)).toEqual([
      {
        key: "source:shop",
        label: "Database · shop",
        before: ["Admins only (1): sales_order."],
        after: ["Hidden from everyone (1): customer_entity."],
      },
      {
        key: "role:support",
        label: "Role · support",
        before: null,
        after: ["A person gets this role when role = 'support'."],
      },
    ]);
    expect(readConfigChangeSummary({ preview: { valid: true } })).toBeNull();
  });

  it("diffs YAML by line", () => {
    expect(diffLines("a\nb\nc\n", "a\nx\nc\n")).toEqual([
      { text: "a", kind: "same" },
      { text: "b", kind: "removed" },
      { text: "x", kind: "added" },
      { text: "c", kind: "same" },
    ]);
    expect(diffLines("", "a\n")).toEqual([{ text: "a", kind: "added" }]);
  });

  it("renders the sentences on the approval card, with the YAML behind a disclosure", () => {
    expect(hasActionChanges(payload)).toBe(true);
    const html = renderToStaticMarkup(createElement(ActionChanges, { payload }));
    expect(html).toContain("Database · shop");
    expect(html).toContain("Now");
    expect(html).toContain("After approval");
    expect(html).toContain("Hidden from everyone (1): customer_entity.");
    expect(html).toContain("Not set up yet.");
    expect(html).toContain("Show the YAML change");
    expect(html).toContain("blocked_tables: [customer_entity]");

    const compact = renderToStaticMarkup(createElement(ActionChanges, { payload, limit: 3 }));
    expect(compact).toContain("After approval");
    expect(compact).not.toContain("Show the YAML change");
  });
});
