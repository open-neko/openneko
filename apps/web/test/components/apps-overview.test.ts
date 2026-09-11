import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  AppsOverview,
  createAppHref,
} from "@/components/records/AppsOverview";

const crm = {
  appId: "crm",
  label: "CRM",
  purpose: "Manage accounts, opportunities, and customer activity.",
  objects: [
    {
      apiName: "account",
      label: "Account",
      pluralLabel: "Accounts",
      recordCount: "5",
      custom: false,
      canCreate: true,
    },
    {
      apiName: "activity",
      label: "Activity",
      pluralLabel: "Activities",
      recordCount: "10",
      custom: false,
      canCreate: true,
    },
  ],
};

describe("AppsOverview", () => {
  it("renders an accessible app ledger with purpose and object summary", () => {
    const html = renderToStaticMarkup(createElement(AppsOverview, {
      apps: [crm],
      canCreate: true,
    }));
    expect(html).toContain("Available to you");
    expect(html).toContain("Manage accounts, opportunities");
    expect(html).toContain("2 objects · Accounts, Activities");
    expect(html).toContain('href="/a/crm"');
    expect(html).toContain(`href="${createAppHref()}"`);
  });

  it("explains creation and approval to an admin with no apps", () => {
    const html = renderToStaticMarkup(createElement(AppsOverview, {
      apps: [],
      canCreate: true,
    }));
    expect(html).toContain("Create your first app");
    expect(html).toContain("Describe the workflow");
    expect(html).toContain("Approve and launch");
    expect(html).toContain("Describe an app");
    expect(html.split(`href="${createAppHref()}"`)).toHaveLength(2);
  });

  it("shows assignment guidance without a creation action to members", () => {
    const html = renderToStaticMarkup(createElement(AppsOverview, {
      apps: [],
      canCreate: false,
    }));
    expect(html).toContain("No apps assigned");
    expect(html).toContain("Contact your OpenNeko administrator");
    expect(html).not.toContain("Describe an app");
    expect(html).not.toContain(createAppHref());
  });

  it("keeps registry failures distinct from an empty app list", () => {
    const html = renderToStaticMarkup(createElement(AppsOverview, {
      apps: [],
      canCreate: true,
      unavailable: true,
    }));
    expect(html).toContain("Apps are temporarily unavailable");
    expect(html).not.toContain("Create your first app");
    expect(html).not.toContain(createAppHref());
  });
});
