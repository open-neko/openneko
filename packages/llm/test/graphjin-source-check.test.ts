import { createServer, type Server } from "node:net";
import { Client } from "pg";
import { createConnection } from "mysql2/promise";
import { beforeAll, describe, expect, it } from "vitest";
import {
  checkSourceConnection,
  mysqlGrantProblems,
  type SourceCheckResult,
} from "../src/graphjin/source-check";

const ROOT_PASSWORD = process.env.SOURCE_CHECK_ROOT_PASSWORD ?? "rootpw";
const PG = process.env.SOURCE_CHECK_PG_PORT ? Number(process.env.SOURCE_CHECK_PG_PORT) : 0;
const PG_TLS = process.env.SOURCE_CHECK_PG_TLS_PORT ? Number(process.env.SOURCE_CHECK_PG_TLS_PORT) : 0;
const MYSQL = process.env.SOURCE_CHECK_MYSQL_PORT ? Number(process.env.SOURCE_CHECK_MYSQL_PORT) : 0;
const MARIADB = process.env.SOURCE_CHECK_MARIADB_PORT ? Number(process.env.SOURCE_CHECK_MARIADB_PORT) : 0;
const PLANTED = "planted-Secret-7731";

function step(result: SourceCheckResult, name: string) {
  return result.findings.find((finding) => finding.step === name)!;
}

function statuses(result: SourceCheckResult): string {
  return result.findings.map((finding) => `${finding.step}:${finding.status}`).join(" ");
}

describe("source check without a database", () => {
  it("says when a database kind has no check yet", async () => {
    const result = await checkSourceConnection({ type: "snowflake", host: "x", database: "d", user: "u", password: "p" });
    expect(result.supported).toBe(false);
    expect(result.ok).toBe(false);
    expect(step(result, "resolve").message).toContain("no source check for snowflake yet");
    expect(step(result, "login").message).toBe("Not checked.");
  });

  it("names the host when it does not resolve, and skips the rest", async () => {
    const result = await checkSourceConnection({ type: "postgres", host: "no-such-host.invalid", database: "d", user: "u", password: PLANTED });
    expect(step(result, "resolve")).toMatchObject({ status: "fail", field: "host" });
    expect(step(result, "resolve").message).toContain("cannot find the host no-such-host.invalid");
    expect(statuses(result)).toBe("resolve:fail port:skip tls:skip login:skip schema:skip read_only:skip");
  });

  it("names the port when nothing listens on it", async () => {
    const server = createServer();
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = (server.address() as { port: number }).port;
    await new Promise<void>((resolve) => server.close(() => resolve()));
    const result = await checkSourceConnection({ type: "mysql", host: "127.0.0.1", port, database: "d", user: "u", password: PLANTED });
    expect(step(result, "port")).toMatchObject({ status: "fail", field: "port" });
    expect(step(result, "port").message).toContain(`refuses connections on port ${port}`);
  });

  it("says when the port answers but the server is not that database", async () => {
    let server: Server | undefined;
    const port = await new Promise<number>((resolve) => {
      server = createServer((socket) => { socket.on("error", () => undefined); socket.end("HTTP/1.1 400 Bad Request\r\n\r\n"); });
      server.listen(0, "127.0.0.1", () => resolve((server!.address() as { port: number }).port));
    });
    try {
      const result = await checkSourceConnection({ type: "postgres", host: "127.0.0.1", port, database: "d", user: "u", password: PLANTED, timeoutMs: 1_000 });
      expect(step(result, "port").status).toBe("ok");
      expect(step(result, "tls")).toMatchObject({ status: "fail", field: "port" });
      expect(step(result, "tls").message).toContain("does not answer like PostgreSQL");
    } finally {
      server?.close();
    }
  });

  it("says when a MySQL port answers like another server", async () => {
    let server: Server | undefined;
    const port = await new Promise<number>((resolve) => {
      server = createServer((socket) => { socket.on("error", () => undefined); socket.end("SSH-2.0-OpenSSH_9.6\r\n"); });
      server.listen(0, "127.0.0.1", () => resolve((server!.address() as { port: number }).port));
    });
    try {
      const result = await checkSourceConnection({ type: "mysql", host: "127.0.0.1", port, database: "d", user: "u", password: PLANTED, timeoutMs: 1_000 });
      expect(step(result, "tls")).toMatchObject({ status: "fail", field: "port" });
      expect(step(result, "tls").message).toContain("does not answer like MySQL or MariaDB");
    } finally {
      server?.close();
    }
  });

  it("reads MySQL grants as read-only only for SELECT, SHOW VIEW and USAGE", () => {
    expect(mysqlGrantProblems(["GRANT USAGE ON *.* TO `r`@`%`", "GRANT SELECT, SHOW VIEW ON `shop`.* TO `r`@`%`"])).toEqual([]);
    expect(mysqlGrantProblems(["GRANT SELECT, INSERT ON `shop`.* TO `w`@`%`"])).toEqual(["INSERT"]);
    expect(mysqlGrantProblems(["GRANT SELECT ON `shop`.* TO `w`@`%` WITH GRANT OPTION"])).toEqual(["GRANT OPTION"]);
    expect(mysqlGrantProblems(["GRANT ALL PRIVILEGES ON *.* TO `root`@`%`"])).toEqual(["ALL PRIVILEGES"]);
  });
});

async function seedPostgres(port: number): Promise<void> {
  const admin = new Client({ host: "127.0.0.1", port, user: "postgres", password: ROOT_PASSWORD, database: "postgres" });
  await admin.connect();
  await admin.query("drop database if exists sales");
  for (const role of ["reader", "nobody", "writer", "appowner"]) await admin.query(`drop role if exists ${role}`);
  await admin.query("create database sales");
  for (const role of ["reader", "nobody", "writer", "appowner"]) await admin.query(`create role ${role} login password '${PLANTED}'`);
  await admin.end();
  const sales = new Client({ host: "127.0.0.1", port, user: "postgres", password: ROOT_PASSWORD, database: "sales" });
  await sales.connect();
  await sales.query("create table orders (id int primary key, total numeric)");
  await sales.query("insert into orders select g, g * 1.5 from generate_series(1, 120) g");
  await sales.query("create table customers (id int primary key)");
  await sales.query("create schema finance");
  await sales.query("create table finance.ledger (id int)");
  await sales.query("analyze");
  await sales.query("grant usage on schema finance to reader, writer");
  await sales.query("grant select on all tables in schema public, finance to reader");
  await sales.query("grant select, insert on orders to writer");
  await sales.query("grant create on schema public to appowner");
  await sales.query("set role appowner");
  await sales.query("create table app_settings (id int)");
  await sales.end();
}

(PG ? describe : describe.skip)("source check against PostgreSQL", () => {
  beforeAll(async () => seedPostgres(PG));
  const base = { type: "postgres", host: "localhost", port: PG, database: "sales" };

  it("passes a read-only login and lists its tables with row estimates", async () => {
    const result = await checkSourceConnection({ ...base, user: "reader", password: PLANTED });
    expect(statuses(result)).toBe("resolve:ok port:ok tls:warn login:ok schema:ok read_only:ok");
    expect(result.ok).toBe(true);
    expect(step(result, "tls").message).toContain("TLS is off");
    expect(result.serverVersion).toMatch(/^PostgreSQL 16/);
    expect(result.tables).toEqual(expect.arrayContaining([
      { name: "orders", rows: 120 },
      { name: "finance.ledger", rows: expect.anything() },
    ]));
    expect(result.tables.map((table) => table.name)).not.toContain("app_settings");
    expect(step(result, "schema").message).toBe("reader can read 3 of the 4 tables. OpenNeko sees only these 3.");
  });

  it("gives the create-login SQL when the password is wrong, and never shows the password", async () => {
    const result = await checkSourceConnection({ ...base, user: "reader", password: `${PLANTED}-wrong` });
    expect(step(result, "login")).toMatchObject({ status: "fail", field: "secretRef" });
    expect(step(result, "login").sql).toEqual(expect.arrayContaining([
      `CREATE ROLE "reader" LOGIN PASSWORD '<choose a password>';`,
      `GRANT SELECT ON ALL TABLES IN SCHEMA public TO "reader";`,
    ]));
    expect(JSON.stringify(result)).not.toContain(PLANTED);
  });

  it("names a database that does not exist", async () => {
    const result = await checkSourceConnection({ ...base, database: "nosuchdb", user: "reader", password: PLANTED });
    expect(step(result, "login")).toMatchObject({ status: "fail", field: "dbname" });
  });

  it("fails a login with no read rights and gives the GRANT SQL", async () => {
    const result = await checkSourceConnection({ ...base, user: "nobody", password: PLANTED });
    expect(step(result, "login").status).toBe("ok");
    expect(step(result, "schema")).toMatchObject({ status: "fail", field: "user" });
    expect(step(result, "schema").message).toContain("cannot read any of the 4 tables");
    expect(step(result, "schema").sql).toEqual(expect.arrayContaining([
      `GRANT SELECT ON ALL TABLES IN SCHEMA "public" TO "nobody";`,
      `GRANT USAGE ON SCHEMA "finance" TO "nobody";`,
    ]));
    expect(result.ok).toBe(false);
  });

  it("fails a login that can change data and gives the REVOKE SQL", async () => {
    const result = await checkSourceConnection({ ...base, user: "writer", password: PLANTED });
    expect(step(result, "read_only")).toMatchObject({ status: "fail", field: "user" });
    expect(step(result, "read_only").message).toContain("can change data in 1 tables (orders)");
    expect(step(result, "read_only").sql).toEqual([`REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA "public" FROM "writer";`]);
  });

  it("fails a login that owns tables", async () => {
    const result = await checkSourceConnection({ ...base, user: "appowner", password: PLANTED });
    expect(step(result, "read_only").status).toBe("fail");
    expect(step(result, "read_only").message).toContain("owns 1 tables (app_settings)");
  });

  it("fails a superuser", async () => {
    const result = await checkSourceConnection({ ...base, user: "postgres", password: ROOT_PASSWORD });
    expect(step(result, "read_only").message).toContain("is a superuser");
  });
});

(PG_TLS ? describe : describe.skip)("source check against PostgreSQL with TLS", () => {
  beforeAll(async () => seedPostgres(PG_TLS));

  it("finds TLS on and says that a self-signed certificate does not verify", async () => {
    const result = await checkSourceConnection({ type: "postgres", host: "localhost", port: PG_TLS, database: "sales", user: "reader", password: PLANTED });
    expect(step(result, "tls").status).toBe("warn");
    expect(step(result, "tls").message).toMatch(/TLS is on \(TLSv1\.[23]\), but OpenNeko's server cannot verify the certificate \(.*SELF_SIGNED.*\)/);
    expect(step(result, "login").status).toBe("ok");
    expect(result.ok).toBe(true);
  });
});

async function seedMysql(port: number, kind: "mysql" | "mariadb"): Promise<void> {
  const root = await createConnection({ host: "127.0.0.1", port, user: "root", password: ROOT_PASSWORD, multipleStatements: true });
  const users = ["reader", "nobody", "writer"];
  await root.query(`DROP DATABASE IF EXISTS shop; CREATE DATABASE shop; ${users.map((user) => `DROP USER IF EXISTS '${user}'@'%';`).join(" ")}`);
  await root.query(users.map((user) => `CREATE USER '${user}'@'%' IDENTIFIED BY '${PLANTED}';`).join(" "));
  await root.query("CREATE TABLE shop.sales_order (entity_id INT PRIMARY KEY) ENGINE=InnoDB; CREATE TABLE shop.admin_user (id INT) ENGINE=InnoDB;");
  await root.query("INSERT INTO shop.sales_order VALUES (1), (2), (3); ANALYZE TABLE shop.sales_order;");
  await root.query("GRANT SELECT, SHOW VIEW ON shop.* TO 'reader'@'%'; GRANT SELECT, INSERT ON shop.* TO 'writer'@'%';");
  if (kind === "mysql") await root.query("FLUSH PRIVILEGES");
  await root.end();
}

for (const [label, kind, port] of [["MySQL", "mysql", MYSQL], ["MariaDB", "mariadb", MARIADB]] as const) {
  (port ? describe : describe.skip)(`source check against ${label}`, () => {
    beforeAll(async () => seedMysql(port, kind));
    const base = { type: kind, host: "127.0.0.1", port, database: "shop" };

    it("passes a read-only login and lists its tables", async () => {
      const result = await checkSourceConnection({ ...base, user: "reader", password: PLANTED });
      expect(statuses(result)).toMatch(/^resolve:ok port:ok tls:(ok|warn) login:ok schema:ok read_only:ok$/);
      expect(result.serverVersion).toContain(label);
      expect(result.tables.map((table) => table.name)).toEqual(["admin_user", "sales_order"]);
      expect(result.tables.find((table) => table.name === "sales_order")?.rows).toBe(3);
    });

    it("finds the TLS that the server offers", async () => {
      const result = await checkSourceConnection({ ...base, user: "reader", password: PLANTED });
      const tls = step(result, "tls");
      if (kind === "mysql") expect(tls.message).toMatch(/TLS is on \(TLSv1\.[23]\), but OpenNeko's server cannot verify/);
      else expect(tls.status === "warn" || tls.status === "ok").toBe(true);
    });

    it("gives the create-login SQL when the password is wrong", async () => {
      const result = await checkSourceConnection({ ...base, user: "reader", password: `${PLANTED}-wrong` });
      expect(step(result, "login")).toMatchObject({ status: "fail", field: "secretRef" });
      expect(step(result, "login").sql).toEqual([
        "CREATE USER 'reader'@'%' IDENTIFIED BY '<choose a password>';",
        "GRANT SELECT, SHOW VIEW ON `shop`.* TO 'reader'@'%';",
      ]);
      expect(JSON.stringify(result)).not.toContain(PLANTED);
    });

    it("fails a login with no read rights and gives the GRANT SQL", async () => {
      const result = await checkSourceConnection({ ...base, user: "nobody", password: PLANTED });
      expect(step(result, "login")).toMatchObject({ status: "fail", field: "user" });
      expect(step(result, "login").sql).toEqual(["GRANT SELECT, SHOW VIEW ON `shop`.* TO 'nobody'@'%';"]);
    });

    it("fails a login that can change data and gives the REVOKE SQL", async () => {
      const result = await checkSourceConnection({ ...base, user: "writer", password: PLANTED });
      expect(step(result, "read_only")).toMatchObject({ status: "fail", field: "user" });
      expect(step(result, "read_only").message).toContain("(INSERT)");
      expect(step(result, "read_only").sql?.[0]).toBe("REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'writer'@'%';");
    });
  });
}

