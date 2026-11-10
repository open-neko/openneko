import { describe, expect, it } from "vitest";
import {
  checkSourceConfigAnswers,
  mapGraphjinConfigErrors,
} from "../src/graphjin/config-problems";

const database = {
  action: "register_source",
  name: "shop",
  kind: "database",
  type: "mysql",
  host: "db.internal",
  port: 3306,
  dbname: "magento",
  user: "neko_ro",
  secretRef: "shop-password",
};

const current = [
  { name: "warehouse", kind: "database", access: { blocked_tables: ["payroll"], admin_tables: [] } },
];

describe("checkSourceConfigAnswers", () => {
  it("passes complete answers", () => {
    expect(
      checkSourceConfigAnswers(database, { secretNames: ["shop-password"], currentSources: current }),
    ).toEqual([]);
  });

  it("names the answer at fault, in plain words", () => {
    const problems = checkSourceConfigAnswers(
      { ...database, name: "Warehouse", host: " ", port: 70_000, dbname: "", user: "", secretRef: "" },
      { secretNames: [], currentSources: JSON.stringify(current) },
    );
    expect(problems).toEqual([
      { field: "name", message: 'A source named "Warehouse" already exists. Choose another name.' },
      { field: "host", message: "The host is empty. Enter the name or address of the database server." },
      { field: "port", message: "The port must be a whole number from 1 to 65535." },
      { field: "dbname", message: "The database name is empty." },
      { field: "user", message: "The login name is empty." },
      { field: "secretRef", message: "Save the password for this login first." },
    ]);
  });

  it("finds a password name that is not saved", () => {
    expect(
      checkSourceConfigAnswers(database, { secretNames: ["other-password"] }),
    ).toEqual([{ field: "secretRef", message: 'No saved password is named "shop-password". Save the password again.' }]);
  });

  it("finds an unknown source and lists the ones that exist", () => {
    expect(
      checkSourceConfigAnswers({ action: "set_source_access", source: "shopp", read: "admin" }, { currentSources: current }),
    ).toEqual([{ field: "source", message: 'No source is named "shopp". The sources are: warehouse.' }]);
    expect(checkSourceConfigAnswers({ action: "set_source_access", source: "" })).toEqual([
      { field: "source", message: "Say which source to change. The source name is empty." },
    ]);
  });

  it("finds a table the source does not have, and accepts a table it hides today", () => {
    const sourceTables = new Map([["warehouse", ["orders", "customers"]]]);
    expect(
      checkSourceConfigAnswers(
        { action: "set_source_tables", source: "warehouse", blockedTables: ["orders", "ordrs"], openTables: ["payroll"] },
        { currentSources: current, sourceTables },
      ),
    ).toEqual([{ field: "blockedTables", message: 'The source "warehouse" has no table named "ordrs".' }]);
    expect(
      checkSourceConfigAnswers(
        { action: "set_source_tables", source: "warehouse", adminTables: ["a", "b"] },
        { currentSources: current, sourceTables },
      ),
    ).toEqual([{ field: "adminTables", message: 'The source "warehouse" has no tables named "a", "b".' }]);
  });

  it("skips the table check when the catalog lists no tables", () => {
    expect(
      checkSourceConfigAnswers(
        { action: "set_source_tables", source: "warehouse", blockedTables: ["anything"] },
        { currentSources: current, sourceTables: new Map() },
      ),
    ).toEqual([]);
  });

  it("checks a role", () => {
    expect(checkSourceConfigAnswers({ action: "add_role", name: "", match: "" })).toEqual([
      { field: "name", message: "The role name is empty." },
      { field: "match", message: "Say which people get this role. The match rule is empty." },
    ]);
  });
});

describe("mapGraphjinConfigErrors", () => {
  const conn = (cause: string) =>
    `database 'shop' (neko_ro@db.internal:3306/magento): connection failed: ${cause}`;

  it("ties a connection failure to the answer that caused it", () => {
    const fields = [
      conn("dial tcp: lookup db.internal on 127.0.0.11:53: no such host"),
      conn("dial tcp 10.0.0.5:3306: connect: connection refused"),
      conn("Error 1045 (28000): Access denied for user 'neko_ro'@'10.0.0.9' (using password: YES)"),
      conn('failed to connect to `user=neko_ro database=magento`: server error: FATAL: password authentication failed for user "neko_ro" (SQLSTATE 28P01)'),
      conn('FATAL: role "neko_ro" does not exist (SQLSTATE 28000)'),
      conn("Error 1049 (42000): Unknown database 'magento'"),
      conn('FATAL: database "magento" does not exist (SQLSTATE 3D000)'),
      conn("tls: failed to verify certificate: x509: certificate signed by unknown authority"),
      conn("something new"),
    ].map((message) => mapGraphjinConfigErrors([message], database)[0]!.field);
    expect(fields).toEqual(["host", "port", "secretRef", "secretRef", "user", "dbname", "dbname", "host", "host"]);
  });

  it("keeps GraphJin's text as detail and says the fix in plain words", () => {
    const [problem] = mapGraphjinConfigErrors([conn("no such host")], database);
    expect(problem).toEqual({
      field: "host",
      message: 'OpenNeko cannot find the host "db.internal". Check the host name.',
      detail: conn("no such host"),
    });
  });

  it("maps config errors from the pinned loader", () => {
    expect(
      mapGraphjinConfigErrors(['sources["shop"].access.read: unsupported access mode "everyone"'], { action: "set_source_access", source: "shop" }),
    ).toEqual([{
      field: "read",
      message: 'GraphJin does not accept this read access: unsupported access mode "everyone".',
      detail: 'sources["shop"].access.read: unsupported access mode "everyone"',
    }]);
    expect(mapGraphjinConfigErrors("sources[0]: name is required", database)[0]!.field).toBe("name");
    expect(mapGraphjinConfigErrors([{ message: "odd" }], database)).toEqual([
      { field: null, message: "GraphJin did not accept this change.", detail: "odd" },
    ]);
    expect(mapGraphjinConfigErrors(null, database)).toEqual([]);
  });
});
