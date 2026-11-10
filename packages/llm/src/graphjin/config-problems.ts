export type ConfigProblem = {
  /** The answer the problem belongs to: a form field or tool argument name. Null when no single answer is at fault. */
  field: string | null;
  message: string;
  /** GraphJin's own text, for experts. */
  detail?: string;
};

type CurrentSource = {
  name: string;
  blocked: string[];
  admin: string[];
};

export type AnswerCheckContext = {
  secretNames?: readonly string[];
  currentSources?: unknown;
  /** Tables the live catalog lists per source name (lower case). */
  sourceTables?: ReadonlyMap<string, readonly string[]>;
};

function text(value: unknown): string {
  const selected = Array.isArray(value) && value.length === 1 ? value[0] : value;
  return typeof selected === "string" ? selected.trim() : "";
}

function names(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((item) => (typeof item === "string" ? item.trim() : "")).filter(Boolean)
    : [];
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function currentSources(value: unknown): CurrentSource[] {
  let list = value;
  if (typeof list === "string") {
    try {
      list = JSON.parse(list);
    } catch {
      list = [];
    }
  }
  if (!Array.isArray(list)) return [];
  return list.flatMap((item) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) return [];
    const record = item as Record<string, unknown>;
    if (typeof record.name !== "string") return [];
    const access =
      record.access && typeof record.access === "object" && !Array.isArray(record.access)
        ? (record.access as Record<string, unknown>)
        : {};
    return [{
      name: record.name,
      blocked: stringList(access.blocked_tables),
      admin: stringList(access.admin_tables),
    }];
  });
}

/**
 * Check the admin's answers before GraphJin sees them. Each problem names the
 * answer at fault and says how to fix it.
 */
export function checkSourceConfigAnswers(
  payload: Record<string, unknown>,
  context: AnswerCheckContext = {},
): ConfigProblem[] {
  const problems: ConfigProblem[] = [];
  const add = (field: string, message: string) => problems.push({ field, message });
  const action = text(payload.action);
  const sources = currentSources(context.currentSources);
  const byName = new Map(sources.map((source) => [source.name.toLowerCase(), source]));

  if (action === "add_role") {
    if (!text(payload.name)) add("name", "The role name is empty.");
    if (!text(payload.match)) add("match", "Say which people get this role. The match rule is empty.");
    return problems;
  }

  if (action === "register_source") {
    const name = text(payload.name);
    if (!name) add("name", "Give the source a name.");
    else if (byName.has(name.toLowerCase())) {
      add("name", `A source named "${name}" already exists. Choose another name.`);
    }
    if ((text(payload.kind) || "database") !== "database") return problems;
    if (!text(payload.host)) add("host", "The host is empty. Enter the name or address of the database server.");
    if (payload.port !== undefined && payload.port !== null && payload.port !== "") {
      const port = Number(payload.port);
      if (!Number.isInteger(port) || port < 1 || port > 65_535) {
        add("port", "The port must be a whole number from 1 to 65535.");
      }
    }
    if (!text(payload.dbname)) add("dbname", "The database name is empty.");
    if (!text(payload.user)) add("user", "The login name is empty.");
    const secretRef = text(payload.secretRef);
    if (!secretRef) add("secretRef", "Save the password for this login first.");
    else if (context.secretNames && !context.secretNames.includes(secretRef)) {
      add("secretRef", `No saved password is named "${secretRef}". Save the password again.`);
    }
    return problems;
  }

  const sourceName = text(payload.source);
  if (!sourceName) {
    add("source", "Say which source to change. The source name is empty.");
    return problems;
  }
  const source = byName.get(sourceName.toLowerCase());
  if (sources.length > 0 && !source) {
    const known = sources.map((item) => item.name).join(", ");
    add("source", `No source is named "${sourceName}". The sources are: ${known}.`);
    return problems;
  }

  if (action === "set_source_tables") {
    const tables = context.sourceTables?.get(sourceName.toLowerCase());
    if (!tables || tables.length === 0) return problems;
    const known = new Set(tables.map((table) => table.toLowerCase()));
    const listed = new Set([...(source?.blocked ?? []), ...(source?.admin ?? [])].map((table) => table.toLowerCase()));
    for (const field of ["blockedTables", "adminTables", "openTables"] as const) {
      const unknown = names(payload[field]).filter(
        (table) => !known.has(table.toLowerCase()) && !listed.has(table.toLowerCase()),
      );
      if (unknown.length === 1) add(field, `The source "${sourceName}" has no table named "${unknown[0]}".`);
      if (unknown.length > 1) {
        add(field, `The source "${sourceName}" has no tables named ${unknown.map((table) => `"${table}"`).join(", ")}.`);
      }
    }
  }
  return problems;
}

const CONNECTION_FAILED = /^database '([^']*)' \(([^@]*)@([^:]*):(\d+)\/([^)]*)\): connection failed: ([\s\S]*)$/;

function connectionProblem(message: string): ConfigProblem | null {
  const match = CONNECTION_FAILED.exec(message);
  if (!match) return null;
  const [, , user, host, port, dbname, cause] = match;
  const lower = cause!.toLowerCase();
  const problem = (field: string, text: string): ConfigProblem => ({ field, message: text, detail: message });
  if (/no such host|server misbehaving|name resolution|nodename nor servname|could not translate host/.test(lower)) {
    return problem("host", `OpenNeko cannot find the host "${host}". Check the host name.`);
  }
  if (/connection refused|connect: no route|i\/o timeout|timed out|network is unreachable/.test(lower)) {
    return problem("port", `Nothing answers on ${host} port ${port}. Check the port, and that the database server accepts connections from OpenNeko.`);
  }
  if (/password authentication failed|access denied for user|authentication failed|invalid password/.test(lower)) {
    return problem("secretRef", `The database refused the login "${user}" with this password. Check the login name and save the password again.`);
  }
  if (/role "[^"]*" does not exist/.test(lower)) {
    return problem("user", `The database has no login named "${user}".`);
  }
  if (/database "[^"]*" does not exist|unknown database/.test(lower)) {
    return problem("dbname", `The server has no database named "${dbname}".`);
  }
  if (/permission denied for database|not have connect privilege/.test(lower)) {
    return problem("user", `The login "${user}" may not connect to the database "${dbname}".`);
  }
  if (/tls|ssl|certificate|x509/.test(lower)) {
    return problem("host", `The secure connection to "${host}" failed. Check the server's TLS settings.`);
  }
  return problem("host", `OpenNeko could not connect to the database "${dbname}" on ${host} port ${port}.`);
}

const ACCESS_FIELD = /^sources\["[^"]*"\]\.access\.(read|write|delete): (.*)$/;

/** Tie each of GraphJin's error texts to the answer it came from. */
export function mapGraphjinConfigErrors(
  errors: unknown,
  payload: Record<string, unknown>,
): ConfigProblem[] {
  let list = errors;
  if (typeof list === "string") list = [list];
  if (!Array.isArray(list)) return [];
  const sourceField = text(payload.action) === "register_source" ? "name" : "source";
  return list.flatMap((item): ConfigProblem[] => {
    const message =
      typeof item === "string"
        ? item
        : item && typeof item === "object" && typeof (item as { message?: unknown }).message === "string"
          ? (item as { message: string }).message
          : "";
    if (!message) return [];
    const connection = connectionProblem(message);
    if (connection) return [connection];
    const access = ACCESS_FIELD.exec(message);
    if (access) {
      return [{ field: access[1]!, message: `GraphJin does not accept this ${access[1]} access: ${access[2]}.`, detail: message }];
    }
    if (/^sources\[\d+\]: name is required/.test(message)) {
      return [{ field: sourceField, message: "Give the source a name.", detail: message }];
    }
    if (/duplicate source names|already exists/.test(message)) {
      return [{ field: sourceField, message: "A source with this name already exists. Choose another name.", detail: message }];
    }
    if (/unsupported database type/.test(message)) {
      return [{ field: "type", message: "GraphJin does not support this database type.", detail: message }];
    }
    if (/system database/i.test(message)) {
      return [{ field: "dbname", message: "This is a system database. Choose the database that holds the business data.", detail: message }];
    }
    return [{ field: null, message: "GraphJin did not accept this change.", detail: message }];
  });
}

export function describeConfigProblems(problems: readonly ConfigProblem[]): string {
  return problems.map((problem) => problem.message).join(" ");
}
