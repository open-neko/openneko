import { describe, expect, it } from "vitest";
import { buildGraphjinConfigUpdate } from "../src/graphjin/config-change";
import {
  describeGraphjinConfig,
  describeGraphjinConfigChange,
} from "../src/graphjin/config-describe";

const shop = {
  name: "shop",
  kind: "database",
  type: "mysql",
  host: "db.example.test",
  port: 3306,
  dbname: "magento",
  user: "neko_ro",
  password: "[REDACTED]",
  read_only: true,
  access: {
    read: "authenticated",
    write: "blocked",
    delete: "blocked",
    blocked_tables: ["admin_user", "oauth_token"],
    admin_tables: ["sales_order"],
  },
};

describe("GraphJin config in plain sentences", () => {
  it("describes a database source, its rules and its tables", () => {
    const [source] = describeGraphjinConfig({ sources: [shop] }).sources;
    expect(source).toEqual({
      name: "shop",
      kind: "database",
      sentences: [
        "A MySQL source: database magento, on db.example.test, port 3306, with the login neko_ro.",
        "OpenNeko only reads it. GraphJin blocks every change to this database.",
        "Who may read: everyone who is signed in.",
        "Who may change or delete: nobody.",
        "Hidden from everyone (2): admin_user, oauth_token.",
        "Admins only (1): sales_order.",
        "Every other table follows the read rule.",
      ],
    });
  });

  it("names GraphJin's defaults and the row rule when access is empty", () => {
    const [source] = describeGraphjinConfig({
      sources: JSON.stringify([{ name: "erp", kind: "database", host: "erp", access: {} }]),
    }).sources;
    expect(source?.sentences).toContain(
      "Who may read: everyone who is signed in, only rows where account_id is their organization.",
    );
    expect(source?.sentences).toContain("GraphJin does not block changes to this database.");
    expect(source?.sentences).toContain("Every table that the login can read follows the read rule.");
  });

  it("shortens a long table list", () => {
    const tables = Array.from({ length: 15 }, (_, index) => `t${index}`);
    const [source] = describeGraphjinConfig({
      sources: [{ ...shop, access: { blocked_tables: tables } }],
    }).sources;
    expect(source?.sentences).toContain(
      "Hidden from everyone (15): t0, t1, t2, t3, t4, t5, t6, t7, t8, t9, t10, t11 and 3 more.",
    );
  });

  it("describes API and file sources and roles", () => {
    const summary = describeGraphjinConfig({
      sources: [
        {
          name: "billing",
          kind: "api",
          read_only: false,
          capabilities: { "api.write": true },
          specs: {
            stripe: { operations: { createRefund: { expose_mutation: true, allowed_roles: ["admin"] } } },
          },
          access: { write: "admin" },
        },
        { name: "docs", kind: "file", backend: "s3", bucket: "acme-docs", prefix: "shared/", read_only: true },
      ],
      roles: [{ Name: "support", Match: "role = 'support'", Comment: "Help desk" }],
    });
    expect(summary.sources[0]?.sentences).toEqual([
      "An API source, described by the OpenAPI documents stripe.",
      "Write calls are on, for the calls named below. Delete calls are off.",
      "The write call stripe/createRefund is open to the roles admin.",
      "Who may read: everyone who is signed in.",
      "Who may change: admins only.",
      "Who may delete: nobody.",
    ]);
    expect(summary.sources[1]?.sentences).toEqual([
      "A file source in the S3 bucket acme-docs, under shared/.",
      "Who may list and read files: everyone who is signed in.",
      "Nobody can change or delete files through OpenNeko.",
    ]);
    expect(summary.roles).toEqual([
      { name: "support", sentence: "A person gets this role when role = 'support'. Note: Help desk" },
    ]);
  });
});

describe("GraphJin config change, before and after", () => {
  it("shows a new source with no before, and keeps the password out", async () => {
    const { update } = await buildGraphjinConfigUpdate(
      {
        action: "register_source",
        name: "warehouse",
        kind: "database",
        type: "postgres",
        host: "wh.example.test",
        port: 5432,
        dbname: "wh",
        user: "reader",
        secretRef: "WH_PASSWORD",
        blockedTables: ["salaries"],
      },
      async () => "planted-pw-7731",
    );
    const change = describeGraphjinConfigChange({
      current: { sources: [{ ...shop, password: "live-pw-0042" }], roles: [] },
      update,
    });
    expect(change.before.sources).toEqual([]);
    expect(change.yaml.before).toBe("");
    expect(change.after.sources.map((source) => source.name)).toEqual(["warehouse"]);
    expect(change.after.sources[0]?.sentences[0]).toBe(
      "A PostgreSQL source: database wh, on wh.example.test, port 5432, with the login reader.",
    );
    expect(change.after.sources[0]?.sentences).toContain("Hidden from everyone (1): salaries.");
    expect(change.yaml.after).toContain("name: warehouse");
    expect(change.yaml.after).toContain("[REDACTED]");
    const all = JSON.stringify(change);
    expect(all).not.toContain("planted-pw-7731");
    expect(all).not.toContain("live-pw-0042");
    expect(all).not.toContain("shop");
  });

  it("shows a table change as the same source before and after", async () => {
    const { update } = await buildGraphjinConfigUpdate({
      action: "set_source_tables",
      source: "shop",
      openTables: ["sales_order"],
      blockedTables: ["customer_entity"],
    });
    const change = describeGraphjinConfigChange({
      current: { sources: JSON.stringify([shop]) },
      update,
    });
    expect(change.before.sources[0]?.sentences).toContain("Admins only (1): sales_order.");
    expect(change.after.sources[0]?.sentences).toContain(
      "Hidden from everyone (3): admin_user, oauth_token, customer_entity.",
    );
    expect(change.after.sources[0]?.sentences.join(" ")).not.toContain("Admins only");
    expect(change.yaml.before).toContain("sales_order");
    expect(change.yaml.after).toContain("customer_entity");
  });

  it("shows a new role", async () => {
    const { update } = await buildGraphjinConfigUpdate({
      action: "add_role",
      name: "support",
      match: "role = 'support'",
    });
    const change = describeGraphjinConfigChange({
      current: { sources: [shop], roles: [{ Name: "finance", Match: "role = 'finance'" }] },
      update,
    });
    expect(change.before.roles).toEqual([]);
    expect(change.after.roles).toEqual([
      { name: "support", sentence: "A person gets this role when role = 'support'." },
    ]);
    expect(change.after.sources).toEqual([]);
  });
});
