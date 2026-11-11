import { lookup } from "node:dns/promises";
import { isIP, Socket } from "node:net";
import { connect as tlsConnect } from "node:tls";
import { Client as PgClient } from "pg";
import { createConnection as createMysqlConnection, type Connection as MysqlConnection, type RowDataPacket } from "mysql2/promise";
import { DATABASE_SOURCE_TYPES, PASSWORD_REQUIRED_MESSAGE } from "./source-types";

export type SourceCheckStep = "resolve" | "port" | "tls" | "login" | "schema" | "read_only";
export type SourceCheckStatus = "ok" | "warn" | "fail" | "skip";

export type SourceCheckFinding = {
  step: SourceCheckStep;
  status: SourceCheckStatus;
  message: string;
  /** The answer at fault: host, port, dbname, user or secretRef. */
  field?: string;
  sql?: string[];
};

export type SourceCheckTable = { name: string; rows: number | null };

export type SourceCheckInput = {
  type: string;
  host: string;
  port?: number;
  database: string;
  user: string;
  password: string;
  timeoutMs?: number;
};

export type SourceCheckResult = {
  ok: boolean;
  supported: boolean;
  type: string;
  host: string;
  port: number;
  database: string;
  user: string;
  serverVersion?: string;
  findings: SourceCheckFinding[];
  tables: SourceCheckTable[];
  /** Readable tables beyond the list limit. */
  moreTables: number;
};

export const SOURCE_CHECK_STEPS: readonly SourceCheckStep[] = ["resolve", "port", "tls", "login", "schema", "read_only"];
export const SOURCE_CHECK_TABLE_LIMIT = 1_000;

const DEFAULT_PORTS: Record<string, number> = { postgres: 5432, mysql: 3306, mariadb: 3306 };
const PLACEHOLDER_PASSWORD = "<choose a password>";

type Dialect = "postgres" | "mysql";

function dialectOf(type: string): Dialect | null {
  if (type === "postgres") return "postgres";
  if (type === "mysql" || type === "mariadb") return "mysql";
  return null;
}

function pgIdent(value: string): string {
  return `"${value.replaceAll('"', '""')}"`;
}

function mysqlIdent(value: string): string {
  return `\`${value.replaceAll("`", "``")}\``;
}

function mysqlString(value: string): string {
  return `'${value.replaceAll("\\", "\\\\").replaceAll("'", "''")}'`;
}

function mysqlAccount(user: string, host: string): string {
  return `${mysqlString(user)}@${mysqlString(host)}`;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function errorCode(error: unknown): string {
  const code = (error as { code?: unknown } | null)?.code;
  return typeof code === "string" ? code : "";
}

export function createReadOnlyLoginSql(type: string, database: string, user: string): string[] {
  if (dialectOf(type) === "mysql") {
    const account = mysqlAccount(user, "%");
    return [
      `CREATE USER ${account} IDENTIFIED BY '${PLACEHOLDER_PASSWORD}';`,
      `GRANT SELECT, SHOW VIEW ON ${mysqlIdent(database)}.* TO ${account};`,
    ];
  }
  const role = pgIdent(user);
  return [
    `CREATE ROLE ${role} LOGIN PASSWORD '${PLACEHOLDER_PASSWORD}';`,
    `GRANT CONNECT ON DATABASE ${pgIdent(database)} TO ${role};`,
    `GRANT USAGE ON SCHEMA public TO ${role};`,
    `GRANT SELECT ON ALL TABLES IN SCHEMA public TO ${role};`,
    `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ${role};`,
  ];
}

function withTimeout<T>(promise: Promise<T>, ms: number, onTimeout: () => void): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  return Promise.race([
    promise,
    new Promise<T>((_, reject) => {
      timer = setTimeout(() => {
        onTimeout();
        reject(Object.assign(new Error("timed out"), { code: "ETIMEDOUT" }));
      }, ms);
    }),
  ]).finally(() => clearTimeout(timer));
}

function openSocket(address: string, port: number, ms: number): Promise<Socket> {
  const socket = new Socket();
  return withTimeout(
    new Promise<Socket>((resolve, reject) => {
      socket.once("connect", () => {
        socket.on("error", () => undefined);
        resolve(socket);
      });
      socket.once("error", reject);
      socket.connect(port, address);
    }),
    ms,
    () => socket.destroy(),
  );
}

function readBytes(socket: Socket, ms: number, enough: (data: Buffer) => boolean): Promise<Buffer> {
  return withTimeout(
    new Promise<Buffer>((resolve, reject) => {
      let data = Buffer.alloc(0);
      const onData = (chunk: Buffer) => {
        data = Buffer.concat([data, chunk]);
        if (enough(data)) {
          socket.off("data", onData);
          resolve(data);
        }
      };
      socket.on("data", onData);
      socket.once("error", reject);
      socket.once("close", () => reject(new Error("the server closed the connection")));
    }),
    ms,
    () => socket.destroy(),
  );
}

type TlsProbe = { offered: boolean; verified: boolean; protocol?: string; reason?: string; serverError?: string };

function tlsHandshake(socket: Socket, host: string, ms: number): Promise<{ verified: boolean; protocol?: string; reason?: string }> {
  const secure = tlsConnect({
    socket,
    servername: isIP(host) ? undefined : host,
    rejectUnauthorized: false,
    minVersion: "TLSv1.2",
  });
  return withTimeout(
    new Promise((resolve, reject) => {
      secure.once("secureConnect", () => {
        const verified = secure.authorized;
        const reason = secure.authorizationError ? String(secure.authorizationError) : undefined;
        const protocol = secure.getProtocol() ?? undefined;
        secure.destroy();
        resolve({ verified, protocol, reason });
      });
      secure.once("error", reject);
    }),
    ms,
    () => secure.destroy(),
  );
}

async function probePostgresTls(address: string, host: string, port: number, ms: number): Promise<TlsProbe> {
  const socket = await openSocket(address, port, ms);
  try {
    const request = Buffer.alloc(8);
    request.writeInt32BE(8, 0);
    request.writeInt32BE(80877103, 4);
    socket.write(request);
    const answer = await readBytes(socket, ms, (data) => data.length >= 1);
    if (answer[0] === 0x4e) return { offered: false, verified: false };
    if (answer[0] !== 0x53) throw new Error("unexpected answer to the TLS request");
    return { offered: true, ...(await tlsHandshake(socket, host, ms)) };
  } finally {
    socket.destroy();
  }
}

const MYSQL_CLIENT_SSL = 0x0800;
const MYSQL_CLIENT_PROTOCOL_41 = 0x0200;

async function probeMysqlTls(address: string, host: string, port: number, ms: number): Promise<TlsProbe> {
  const socket = await openSocket(address, port, ms);
  try {
    const greeting = (data: Buffer) => data.length >= 5 && (data[4] === 0x0a || data[4] === 0xff);
    const packet = await readBytes(socket, ms, (data) =>
      (data.length >= 5 && !greeting(data)) || (data.length >= 4 && data.length >= 4 + data.readUIntLE(0, 3)));
    if (!greeting(packet)) throw new Error("unexpected greeting");
    const length = packet.readUIntLE(0, 3);
    const body = packet.subarray(4, 4 + length);
    if (body[0] === 0xff) {
      return { offered: false, verified: false, serverError: body.subarray(3).toString("utf8").replace(/^#[0-9A-Z]{5}/, "") };
    }
    const versionEnd = body.indexOf(0, 1);
    const capabilityOffset = versionEnd + 1 + 4 + 8 + 1;
    const capabilities = body.readUInt16LE(capabilityOffset);
    const charset = body[capabilityOffset + 2] ?? 0x21;
    if ((capabilities & MYSQL_CLIENT_SSL) === 0) return { offered: false, verified: false };
    const request = Buffer.alloc(4 + 32);
    request.writeUIntLE(32, 0, 3);
    request[3] = 1;
    request.writeUInt32LE(MYSQL_CLIENT_SSL | MYSQL_CLIENT_PROTOCOL_41, 4);
    request.writeUInt32LE(16 * 1024 * 1024, 8);
    request[12] = charset;
    socket.write(request);
    return { offered: true, ...(await tlsHandshake(socket, host, ms)) };
  } finally {
    socket.destroy();
  }
}

function skipRest(findings: SourceCheckFinding[], message = "Not checked, because an earlier step failed."): void {
  const done = new Set(findings.map((finding) => finding.step));
  for (const step of SOURCE_CHECK_STEPS) {
    if (!done.has(step)) findings.push({ step, status: "skip", message });
  }
}

type LoginFailure = { message: string; field?: string; sql?: string[] };

function postgresLoginFailure(error: unknown, input: Required<Pick<SourceCheckInput, "type" | "database" | "user">>): LoginFailure {
  const code = errorCode(error);
  const { database, user } = input;
  switch (code) {
    case "28P01":
      return {
        message: `The database refused the login ${user} with this password. Save the right password. If the login does not exist yet, ask the database administrator to create it as a read-only login.`,
        field: "secretRef",
        sql: createReadOnlyLoginSql("postgres", database, user),
      };
    case "28000":
      return {
        message: `The database does not let ${user} sign in from OpenNeko's server. Ask the database administrator to allow this login from OpenNeko's address in pg_hba.conf. The server said: ${errorText(error)}.`,
        field: "user",
      };
    case "3D000":
      return { message: `The server has no database named ${database}. Check the database name.`, field: "dbname" };
    case "42501":
      return {
        message: `${user} may not connect to the database ${database}.`,
        field: "user",
        sql: [`GRANT CONNECT ON DATABASE ${pgIdent(database)} TO ${pgIdent(user)};`],
      };
    default:
      if (/certificate|ssl|tls/i.test(errorText(error))) {
        return { message: `The TLS connection failed: ${errorText(error)}. Ask the database administrator to check the server's TLS setup.` };
      }
      return { message: `OpenNeko could not sign in as ${user}: ${errorText(error)}.` };
  }
}

function mysqlLoginFailure(error: unknown, input: Required<Pick<SourceCheckInput, "type" | "database" | "user">>): LoginFailure {
  const errno = (error as { errno?: unknown } | null)?.errno;
  const { type, database, user } = input;
  switch (errno) {
    case 1045:
      return {
        message: `The database refused the login ${user} with this password. Save the right password. If the login does not exist yet, ask the database administrator to create it as a read-only login.`,
        field: "secretRef",
        sql: createReadOnlyLoginSql(type, database, user),
      };
    case 1130:
      return {
        message: `The database does not let ${user} sign in from OpenNeko's server. Ask the database administrator to create the login for OpenNeko's address, or for any address ('%').`,
        field: "user",
        sql: createReadOnlyLoginSql(type, database, user),
      };
    case 1049:
      return { message: `The server has no database named ${database}. Check the database name.`, field: "dbname" };
    case 1044:
      return {
        message: `${user} may not read the database ${database}.`,
        field: "user",
        sql: [`GRANT SELECT, SHOW VIEW ON ${mysqlIdent(database)}.* TO ${mysqlAccount(user, "%")};`],
      };
    default:
      return { message: `OpenNeko could not sign in as ${user}: ${errorText(error)}.` };
  }
}

function tableLabel(schema: string, table: string): string {
  return schema === "public" ? table : `${schema}.${table}`;
}

function rowsOf(value: unknown): number | null {
  if (value === null || value === undefined) return null;
  const count = Number(value);
  return Number.isFinite(count) && count >= 0 ? count : null;
}

function quoteList(names: readonly string[], max = 5): string {
  const shown = names.slice(0, max).join(", ");
  return names.length > max ? `${shown} and ${names.length - max} more` : shown;
}

type ReadCheck = { tables: SourceCheckTable[]; total: number; findings: SourceCheckFinding[]; serverVersion?: string };

async function checkPostgresGrants(client: PgClient, input: SourceCheckInput): Promise<ReadCheck> {
  const findings: SourceCheckFinding[] = [];
  const { rows } = await client.query<{ schema: string; name: string; rows: string | null; readable: boolean; writable: boolean; owned: boolean; base: boolean }>(`
    select n.nspname as schema, c.relname as name,
           case when c.reltuples < 0 then null else c.reltuples::bigint end as rows,
           has_schema_privilege(n.oid, 'USAGE') and has_table_privilege(c.oid, 'SELECT') as readable,
           c.relkind in ('r', 'p') and has_table_privilege(c.oid, 'INSERT, UPDATE, DELETE, TRUNCATE') as writable,
           pg_has_role(c.relowner, 'USAGE') as owned,
           c.relkind in ('r', 'p') as base
      from pg_class c join pg_namespace n on n.oid = c.relnamespace
     where c.relkind in ('r', 'p', 'v', 'm', 'f')
       and n.nspname not in ('pg_catalog', 'information_schema')
       and n.nspname not like 'pg\\_%'
     order by n.nspname = 'public' desc, n.nspname, c.relname`);
  const readable = rows.filter((row) => row.readable);
  const tables = readable.slice(0, SOURCE_CHECK_TABLE_LIMIT).map((row) => ({ name: tableLabel(row.schema, row.name), rows: rowsOf(row.rows) }));
  const role = pgIdent(input.user);
  if (rows.length === 0) {
    findings.push({ step: "schema", status: "warn", message: `The database ${input.database} has no tables yet.` });
  } else if (readable.length === 0) {
    const schemas = [...new Set(rows.map((row) => row.schema))];
    findings.push({
      step: "schema",
      status: "fail",
      field: "user",
      message: `${input.user} signs in, but cannot read any of the ${rows.length} tables in ${input.database}.`,
      sql: schemas.flatMap((schema) => [
        `GRANT USAGE ON SCHEMA ${pgIdent(schema)} TO ${role};`,
        `GRANT SELECT ON ALL TABLES IN SCHEMA ${pgIdent(schema)} TO ${role};`,
      ]),
    });
  } else {
    const hidden = rows.length - readable.length;
    findings.push({
      step: "schema",
      status: "ok",
      message: hidden > 0
        ? `${input.user} can read ${readable.length} of the ${rows.length} tables. OpenNeko sees only these ${readable.length}.`
        : `${input.user} can read all ${readable.length} tables.`,
    });
  }

  const { rows: [roleRow] } = await client.query<{ rolsuper: boolean; rolbypassrls: boolean }>(
    "select rolsuper, rolbypassrls from pg_roles where rolname = current_user",
  );
  const owned = rows.filter((row) => row.base && row.owned).map((row) => tableLabel(row.schema, row.name));
  const writable = rows.filter((row) => row.writable && !row.owned).map((row) => ({ schema: row.schema, label: tableLabel(row.schema, row.name) }));
  if (roleRow?.rolsuper) {
    findings.push({
      step: "read_only",
      status: "fail",
      field: "user",
      message: `${input.user} is a superuser, so it can change any data. Use a separate read-only login.`,
      sql: createReadOnlyLoginSql("postgres", input.database, `${input.user}_reader`),
    });
  } else if (owned.length > 0) {
    findings.push({
      step: "read_only",
      status: "fail",
      field: "user",
      message: `${input.user} owns ${owned.length} tables (${quoteList(owned)}), so it can always change them. Use a separate read-only login.`,
      sql: createReadOnlyLoginSql("postgres", input.database, `${input.user}_reader`),
    });
  } else if (writable.length > 0) {
    const schemas = [...new Set(writable.map((row) => row.schema))];
    findings.push({
      step: "read_only",
      status: "fail",
      field: "user",
      message: `${input.user} can change data in ${writable.length} tables (${quoteList(writable.map((row) => row.label))}). OpenNeko needs a read-only login.`,
      sql: schemas.map((schema) => `REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA ${pgIdent(schema)} FROM ${role};`),
    });
  } else {
    findings.push({ step: "read_only", status: "ok", message: `${input.user} can change no data.` });
  }
  return { tables, total: readable.length, findings };
}

const MYSQL_READ_PRIVILEGES = new Set(["SELECT", "SHOW VIEW", "USAGE"]);

export function mysqlGrantProblems(grants: readonly string[]): string[] {
  const problems = new Set<string>();
  for (const grant of grants) {
    const match = /^GRANT\s+(.+?)\s+ON\s+/i.exec(grant);
    if (!match) continue;
    if (/WITH\s+GRANT\s+OPTION/i.test(grant)) problems.add("GRANT OPTION");
    for (const privilege of match[1]!.split(",").map((value) => value.trim().toUpperCase().replace(/\s*\(.*\)$/, ""))) {
      if (privilege && !MYSQL_READ_PRIVILEGES.has(privilege)) problems.add(privilege);
    }
  }
  return [...problems];
}

async function checkMysqlGrants(connection: MysqlConnection, input: SourceCheckInput): Promise<ReadCheck> {
  const findings: SourceCheckFinding[] = [];
  const [tableRows] = await connection.query<RowDataPacket[]>(
    "SELECT TABLE_NAME AS name, TABLE_ROWS AS row_count FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME",
    [input.database],
  );
  const tables = tableRows.slice(0, SOURCE_CHECK_TABLE_LIMIT).map((row) => ({ name: String(row.name), rows: rowsOf(row.row_count) }));
  const [[accountRow]] = await connection.query<RowDataPacket[]>("SELECT CURRENT_USER() AS account");
  const account = String(accountRow?.account ?? `${input.user}@%`);
  const at = account.lastIndexOf("@");
  const quotedAccount = mysqlAccount(account.slice(0, at), account.slice(at + 1));
  if (tableRows.length === 0) {
    findings.push({
      step: "schema",
      status: "fail",
      field: "user",
      message: `${input.user} signs in, but sees no tables in ${input.database}. Either the database is empty or the login may not read it.`,
      sql: [`GRANT SELECT, SHOW VIEW ON ${mysqlIdent(input.database)}.* TO ${quotedAccount};`],
    });
  } else {
    findings.push({ step: "schema", status: "ok", message: `${input.user} can read ${tableRows.length} tables.` });
  }
  const [grantRows] = await connection.query<RowDataPacket[]>("SHOW GRANTS FOR CURRENT_USER()");
  const grants = grantRows.map((row) => String(Object.values(row)[0] ?? ""));
  const problems = mysqlGrantProblems(grants);
  if (problems.length > 0) {
    findings.push({
      step: "read_only",
      status: "fail",
      field: "user",
      message: `${input.user} holds rights beyond reading (${quoteList(problems)}). OpenNeko needs a read-only login.`,
      sql: [
        `REVOKE ALL PRIVILEGES, GRANT OPTION FROM ${quotedAccount};`,
        `GRANT SELECT, SHOW VIEW ON ${mysqlIdent(input.database)}.* TO ${quotedAccount};`,
      ],
    });
  } else {
    findings.push({ step: "read_only", status: "ok", message: `${input.user} holds read rights only.` });
  }
  return { tables, total: tableRows.length, findings };
}

export async function checkSourceConnection(input: SourceCheckInput): Promise<SourceCheckResult> {
  const type = input.type.trim().toLowerCase();
  const host = input.host.trim();
  const port = input.port ?? DEFAULT_PORTS[type] ?? 0;
  const ms = input.timeoutMs ?? 5_000;
  const findings: SourceCheckFinding[] = [];
  const result: SourceCheckResult = {
    ok: false,
    supported: true,
    type,
    host,
    port,
    database: input.database.trim(),
    user: input.user.trim(),
    findings,
    tables: [],
    moreTables: 0,
  };
  const scrub = (finding: SourceCheckFinding): SourceCheckFinding =>
    input.password ? { ...finding, message: finding.message.split(input.password).join("[REDACTED]") } : finding;
  const finish = () => {
    skipRest(findings);
    result.findings = findings.map(scrub);
    result.ok = !findings.some((finding) => finding.status === "fail");
    return result;
  };
  const dialect = dialectOf(type);
  if (!dialect) {
    result.supported = false;
    findings.push({
      step: "resolve",
      status: "skip",
      field: "type",
      message: `OpenNeko has no source check for ${type || "this database kind"} yet. It checks ${DATABASE_SOURCE_TYPES.join(", ")}.`,
    });
    skipRest(findings, "Not checked.");
    result.findings = findings;
    return result;
  }
  const missing = (["host", "database", "user"] as const).filter((key) => !result[key]);
  if (missing.length > 0) {
    findings.push({
      step: "resolve",
      status: "fail",
      field: missing[0] === "database" ? "dbname" : missing[0],
      message: `Enter the ${missing.map((key) => (key === "user" ? "login" : key)).join(", ")} first.`,
    });
    return finish();
  }

  let address: string;
  try {
    address = (await withTimeout(lookup(host), ms, () => undefined)).address;
    findings.push({ step: "resolve", status: "ok", message: isIP(host) ? `${host} is an IP address.` : `${host} resolves to ${address}.` });
  } catch {
    findings.push({ step: "resolve", status: "fail", field: "host", message: `OpenNeko's server cannot find the host ${host}. Check the host name, or enter the IP address.` });
    return finish();
  }

  try {
    (await openSocket(address, port, ms)).destroy();
    findings.push({ step: "port", status: "ok", message: `Port ${port} answers.` });
  } catch (error) {
    const code = errorCode(error);
    findings.push({
      step: "port",
      status: "fail",
      field: "port",
      message: code === "ECONNREFUSED"
        ? `${host} refuses connections on port ${port}. Check the port, and check that the database listens on this address.`
        : code === "ETIMEDOUT"
          ? `No answer from ${host} on port ${port} within ${Math.round(ms / 1000)} seconds. A firewall may block OpenNeko's server, or the port is wrong.`
          : `OpenNeko's server cannot reach ${host} on port ${port}: ${errorText(error)}.`,
    });
    return finish();
  }

  let tls: TlsProbe = { offered: false, verified: false };
  try {
    tls = dialect === "postgres"
      ? await probePostgresTls(address, host, port, ms)
      : await probeMysqlTls(address, host, port, ms);
    findings.push(
      tls.serverError
        ? { step: "tls", status: "skip", message: "Not checked, because the server refused the connection before TLS. The login step says why." }
        : !tls.offered
          ? { step: "tls", status: "warn", message: "TLS is off. The login and the data cross the network unencrypted. Ask the database administrator to turn on TLS." }
          : tls.verified
            ? { step: "tls", status: "ok", message: `TLS is on (${tls.protocol ?? "TLS"}) and the certificate is valid for ${host}.` }
            : { step: "tls", status: "warn", message: `TLS is on (${tls.protocol ?? "TLS"}), but OpenNeko's server cannot verify the certificate (${tls.reason ?? "unknown reason"}). The data is encrypted; the server's identity is not proven.` },
    );
  } catch (error) {
    findings.push({
      step: "tls",
      status: "fail",
      message: `The server does not answer like ${dialect === "postgres" ? "PostgreSQL" : "MySQL or MariaDB"} on port ${port} (${errorText(error)}). Check the database type and the port.`,
      field: "port",
    });
    return finish();
  }

  if (!input.password) {
    findings.push({ step: "login", status: "fail", field: "secretRef", message: PASSWORD_REQUIRED_MESSAGE });
    return finish();
  }
  const naming = { type, database: result.database, user: result.user };
  const servername = isIP(host) ? undefined : host;
  if (dialect === "postgres") {
    const client = new PgClient({
      host: address,
      port,
      database: result.database,
      user: result.user,
      password: input.password,
      ssl: tls.offered ? { rejectUnauthorized: false, servername } : false,
      connectionTimeoutMillis: ms * 2,
      statement_timeout: ms * 2,
      application_name: "openneko-source-check",
    });
    try {
      await client.connect();
    } catch (error) {
      findings.push({ step: "login", status: "fail", ...postgresLoginFailure(error, naming) });
      await client.end().catch(() => undefined);
      return finish();
    }
    try {
      const { rows: [version] } = await client.query<{ version: string }>("select current_setting('server_version') as version");
      result.serverVersion = `PostgreSQL ${version?.version ?? "unknown"}`;
      findings.push({ step: "login", status: "ok", message: `${result.user} signs in to ${result.serverVersion}.` });
      const read = await checkPostgresGrants(client, { ...input, ...naming });
      findings.push(...read.findings);
      result.tables = read.tables;
      result.moreTables = Math.max(0, read.total - read.tables.length);
    } catch (error) {
      findings.push({ step: "schema", status: "fail", message: `OpenNeko signed in, but could not read the schema: ${errorText(error)}.` });
    } finally {
      await client.end().catch(() => undefined);
    }
    return finish();
  }

  let connection: MysqlConnection;
  try {
    connection = await createMysqlConnection({
      host: address,
      port,
      database: result.database,
      user: result.user,
      password: input.password,
      connectTimeout: ms * 2,
      multipleStatements: false,
      ...(tls.offered ? { ssl: { rejectUnauthorized: false } } : {}),
    });
  } catch (error) {
    findings.push({ step: "login", status: "fail", ...mysqlLoginFailure(error, naming) });
    return finish();
  }
  try {
    const [[version]] = await connection.query<RowDataPacket[]>("SELECT VERSION() AS version");
    const text = String(version?.version ?? "unknown");
    result.serverVersion = /mariadb/i.test(text) ? `MariaDB ${text}` : `MySQL ${text}`;
    findings.push({ step: "login", status: "ok", message: `${result.user} signs in to ${result.serverVersion}.` });
    const read = await checkMysqlGrants(connection, { ...input, ...naming });
    findings.push(...read.findings);
    result.tables = read.tables;
    result.moreTables = Math.max(0, read.total - read.tables.length);
  } catch (error) {
    findings.push({ step: "schema", status: "fail", message: `OpenNeko signed in, but could not read the schema: ${errorText(error)}.` });
  } finally {
    await connection.end().catch(() => undefined);
  }
  return finish();
}

export type SourceCheckAnswers = {
  type?: unknown;
  host?: unknown;
  port?: unknown;
  dbname?: unknown;
  user?: unknown;
  secretRef?: unknown;
};

function answer(value: unknown): string {
  const selected = Array.isArray(value) && value.length === 1 ? value[0] : value;
  return typeof selected === "string" ? selected.trim() : "";
}

export async function checkSourceForOrg(input: {
  orgId: string;
  answers: SourceCheckAnswers;
  password?: string;
  timeoutMs?: number;
}): Promise<SourceCheckResult> {
  const { answers } = input;
  const type = answer(answers.type) || "postgres";
  const port = typeof answers.port === "number" ? answers.port : Number(answer(answers.port)) || undefined;
  let password = input.password ?? "";
  const secretRef = answer(answers.secretRef);
  if (!password && secretRef) {
    const { and, data_source_secret, db, eq } = await import("@neko/db");
    const [row] = await db()
      .select({ valueEnc: data_source_secret.value_enc })
      .from(data_source_secret)
      .where(and(eq(data_source_secret.org_id, input.orgId), eq(data_source_secret.name, secretRef)))
      .limit(1);
    if (!row) {
      const result = await checkSourceConnection({ type, host: answer(answers.host), port, database: answer(answers.dbname), user: answer(answers.user), password: "", timeoutMs: input.timeoutMs });
      const login = result.findings.find((finding) => finding.step === "login");
      if (login?.status === "fail") login.message = `No stored password is named "${secretRef}". Save the password in the card first.`;
      return result;
    }
    const { maybeDecryptSecret } = await import("@neko/secret-crypt");
    password = maybeDecryptSecret(row.valueEnc);
  }
  return checkSourceConnection({
    type,
    host: answer(answers.host),
    port,
    database: answer(answers.dbname),
    user: answer(answers.user),
    password,
    timeoutMs: input.timeoutMs,
  });
}
