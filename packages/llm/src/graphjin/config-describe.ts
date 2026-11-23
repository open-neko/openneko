import { parseDocument, stringify } from "yaml";
import { redactSourceConfig } from "./config-redact";
import { mergeGraphjinSourceConfigUpdate } from "./persist-source-config";

export type SourceDescription = {
  name: string;
  kind: string;
  sentences: string[];
};

export type RoleDescription = {
  name: string;
  sentence: string;
};

export type ConfigDescription = {
  sources: SourceDescription[];
  roles: RoleDescription[];
};

export type ConfigChangeDescription = {
  before: ConfigDescription;
  after: ConfigDescription;
  yaml: { before: string; after: string };
};

type Item = Record<string, unknown>;

const TABLES_SHOWN = 12;

const DATABASE_NAMES: Record<string, string> = {
  postgres: "PostgreSQL",
  mysql: "MySQL",
  mariadb: "MariaDB",
  mssql: "SQL Server",
  oracle: "Oracle",
  sqlite: "SQLite",
  snowflake: "Snowflake",
};

const MODE_DEFAULTS: Record<string, { read: string; write: string; delete: string }> = {
  database: { read: "account", write: "blocked", delete: "blocked" },
  api: { read: "authenticated", write: "blocked", delete: "blocked" },
};

function isItem(value: unknown): value is Item {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function parsed(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

function field(item: Item, key: string): unknown {
  if (key in item) return item[key];
  const lower = key.toLowerCase();
  const found = Object.keys(item).find((candidate) => candidate.toLowerCase() === lower);
  return found === undefined ? undefined : item[found];
}

function text(item: Item, key: string): string {
  const value = field(item, key);
  return typeof value === "string" ? value.trim() : typeof value === "number" ? String(value) : "";
}

function names(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry).trim()).filter(Boolean)
    : [];
}

function list(values: string[]): string {
  if (values.length <= TABLES_SHOWN) return values.join(", ");
  return `${values.slice(0, TABLES_SHOWN).join(", ")} and ${values.length - TABLES_SHOWN} more`;
}

function who(mode: string, access: Item): string {
  switch (mode) {
    case "public":
      return "everyone, also people who are not signed in";
    case "authenticated":
      return "everyone who is signed in";
    case "account": {
      const column = text(access, "namespace_column") || "account_id";
      return `everyone who is signed in, only rows where ${column} is their organization`;
    }
    case "owner": {
      const column = text(access, "owner_column") || "user_id";
      return `everyone who is signed in, only rows where ${column} is their own user`;
    }
    case "admin":
      return "admins only";
    case "blocked":
      return "nobody";
    default:
      return `the "${mode}" rule`;
  }
}

function accessSentences(kind: string, access: Item): string[] {
  const defaults = MODE_DEFAULTS[kind] ?? MODE_DEFAULTS.api!;
  const read = text(access, "read") || defaults.read;
  const write = text(access, "write") || defaults.write;
  const remove = text(access, "delete") || defaults.delete;
  const out = [`Who may read: ${who(read, access)}.`];
  if (write === remove) {
    out.push(`Who may change or delete: ${who(write, access)}.`);
  } else {
    out.push(`Who may change: ${who(write, access)}.`);
    out.push(`Who may delete: ${who(remove, access)}.`);
  }
  return out;
}

function tableSentences(access: Item): string[] {
  const out: string[] = [];
  const blocked = names(field(access, "blocked_tables"));
  const admin = names(field(access, "admin_tables"));
  const open = names(field(access, "public_tables"));
  if (blocked.length) out.push(`Hidden from everyone (${blocked.length}): ${list(blocked)}.`);
  if (admin.length) out.push(`Admins only (${admin.length}): ${list(admin)}.`);
  if (open.length) {
    out.push(`Open to everyone, also without sign-in (${open.length}): ${list(open)}.`);
  }
  out.push(
    out.length > 0
      ? "Every other table follows the read rule."
      : "Every table that the login can read follows the read rule.",
  );
  const grants = Array.isArray(field(access, "grants")) ? (field(access, "grants") as unknown[]) : [];
  for (const grant of grants) {
    if (!isItem(grant)) continue;
    const role = text(grant, "role");
    const tables = (Array.isArray(field(grant, "tables")) ? (field(grant, "tables") as unknown[]) : [])
      .filter(isItem)
      .map((table) => text(table, "name"))
      .filter(Boolean);
    if (role && tables.length) out.push(`Role ${role} may also read: ${list(tables)}.`);
  }
  return out;
}

function databaseSentences(source: Item): string[] {
  const type = text(source, "type").toLowerCase() || "postgres";
  const product = DATABASE_NAMES[type] ?? type;
  const place = [
    text(source, "dbname") ? `database ${text(source, "dbname")}` : "",
    text(source, "host") ? `on ${text(source, "host")}` : "",
    text(source, "port") && text(source, "port") !== "0" ? `port ${text(source, "port")}` : "",
  ].filter(Boolean);
  const user = text(source, "user");
  const first = [`A ${product} source`, place.join(", ")].filter(Boolean).join(": ");
  const out = [`${first}${user ? `, with the login ${user}` : ""}.`];
  if (field(source, "enable_tls") === true) out.push("The connection uses TLS.");
  out.push(
    field(source, "read_only") === true
      ? "OpenNeko only reads it. GraphJin blocks every change to this database."
      : "GraphJin does not block changes to this database.",
  );
  const access = isItem(field(source, "access")) ? (field(source, "access") as Item) : {};
  return [...out, ...accessSentences("database", access), ...tableSentences(access)];
}

function writeOperations(source: Item): string[] {
  const out: string[] = [];
  const specs = isItem(field(source, "specs")) ? (field(source, "specs") as Item) : {};
  for (const [spec, config] of Object.entries(specs)) {
    if (!isItem(config) || !isItem(field(config, "operations"))) continue;
    for (const [operation, settings] of Object.entries(field(config, "operations") as Item)) {
      if (!isItem(settings) || field(settings, "expose_mutation") !== true) continue;
      const roles = names(field(settings, "allowed_roles"));
      out.push(
        `The write call ${spec}/${operation} is open to ${roles.length ? `the roles ${roles.join(", ")}` : "no role"}.`,
      );
    }
  }
  return out;
}

function apiSentences(source: Item): string[] {
  const specs = isItem(field(source, "specs")) ? Object.keys(field(source, "specs") as Item) : [];
  const out = [
    specs.length
      ? `An API source, described by the OpenAPI documents ${specs.join(", ")}.`
      : "An API source, described by OpenAPI documents.",
  ];
  const capabilities = isItem(field(source, "capabilities")) ? (field(source, "capabilities") as Item) : {};
  if (field(source, "read_only") === true || capabilities["api.write"] !== true) {
    out.push("OpenNeko only reads it. GraphJin blocks every write call.");
  } else {
    out.push(
      capabilities["api.delete"] === true
        ? "Write and delete calls are on, for the calls named below."
        : "Write calls are on, for the calls named below. Delete calls are off.",
    );
    out.push(...writeOperations(source));
  }
  const access = isItem(field(source, "access")) ? (field(source, "access") as Item) : {};
  return [...out, ...accessSentences("api", access)];
}

function fileSentences(source: Item): string[] {
  const backend = text(source, "backend") || "local";
  const bucket = text(source, "bucket");
  const prefix = text(source, "prefix");
  const where =
    backend === "local"
      ? "in OpenNeko's own file storage"
      : `in the ${backend === "gcs" ? "Google Cloud Storage" : "S3"} bucket ${bucket || "(no bucket set)"}${prefix ? `, under ${prefix}` : ""}`;
  const capabilities = isItem(field(source, "capabilities")) ? (field(source, "capabilities") as Item) : {};
  const writes = capabilities["files.write"] === true || capabilities["files.delete"] === true;
  const access = isItem(field(source, "access")) ? (field(source, "access") as Item) : {};
  const read = text(access, "read") || "authenticated";
  return [
    `A file source ${where}.`,
    `Who may list and read files: ${who(read, access)}.`,
    writes && field(source, "read_only") !== true
      ? "GraphJin lets callers change files here."
      : "Nobody can change or delete files through OpenNeko.",
  ];
}

export function describeGraphjinSource(source: Item): SourceDescription {
  const name = text(source, "name");
  const kind = text(source, "kind") || "database";
  const sentences =
    kind === "database"
      ? databaseSentences(source)
      : kind === "api"
        ? apiSentences(source)
        : kind === "file"
          ? fileSentences(source)
          : [`A ${kind} source. OpenNeko does not describe this kind in detail.`];
  if (field(source, "default") === true) sentences.push("It is the default source.");
  return { name, kind, sentences };
}

export function describeGraphjinRole(role: Item): RoleDescription {
  const name = text(role, "name");
  const match = text(role, "match");
  const comment = text(role, "comment");
  const sentence = [
    match ? `A person gets this role when ${match}.` : "No rule gives a person this role.",
    comment ? `Note: ${comment}` : "",
  ]
    .filter(Boolean)
    .join(" ");
  return { name, sentence };
}

/** Plain sentences for a GraphJin config. The input must already be redacted. */
export function describeGraphjinConfig(config: {
  sources?: unknown;
  roles?: unknown;
}): ConfigDescription {
  const sources = parsed(config.sources);
  const roles = parsed(config.roles);
  return {
    sources: (Array.isArray(sources) ? sources : [])
      .filter(isItem)
      .map(describeGraphjinSource)
      .filter((source) => source.name),
    roles: (Array.isArray(roles) ? roles : [])
      .filter(isItem)
      .map(describeGraphjinRole)
      .filter((role) => role.name),
  };
}

function touchedNames(update: Item): { sources: Set<string>; roles: Set<string> } {
  const sources = new Set<string>();
  for (const key of ["update_sources", "source_patches"]) {
    for (const entry of Array.isArray(update[key]) ? (update[key] as unknown[]) : []) {
      if (isItem(entry) && typeof entry.name === "string") sources.add(entry.name.trim().toLowerCase());
    }
  }
  for (const entry of Array.isArray(update.remove_sources) ? update.remove_sources : []) {
    if (typeof entry === "string") sources.add(entry.trim().toLowerCase());
  }
  const roles = new Set<string>();
  for (const entry of Array.isArray(update.roles) ? update.roles : []) {
    if (isItem(entry) && typeof entry.name === "string") roles.add(entry.name.trim().toLowerCase());
  }
  return { sources, roles };
}

function lowerKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(lowerKeys);
  if (!isItem(value)) return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key.toLowerCase(), item]));
}

function named(items: unknown, wanted: Set<string>): Item[] {
  return (Array.isArray(items) ? items : [])
    .filter(isItem)
    .filter((item) => wanted.has(text(item, "name").toLowerCase()));
}

/**
 * The sources and roles that a change touches, before and after, as
 * sentences and as redacted YAML. The update may hold a resolved password:
 * the merge seals it to a reference and the result is redacted again.
 */
export function describeGraphjinConfigChange(input: {
  current: { sources?: unknown; roles?: unknown };
  update: Record<string, unknown>;
}): ConfigChangeDescription {
  const current = {
    sources: redactSourceConfig(parsed(input.current.sources) ?? []),
    roles: lowerKeys(parsed(input.current.roles) ?? []),
  };
  const merged = parseDocument(
    mergeGraphjinSourceConfigUpdate(stringify(current), input.update),
  ).toJS() as Item;
  const next = {
    sources: redactSourceConfig(merged.sources ?? []),
    roles: redactSourceConfig(merged.roles ?? []),
  };
  const touched = touchedNames(input.update);
  const pick = (config: { sources: unknown; roles: unknown }) => ({
    sources: named(config.sources, touched.sources),
    roles: named(config.roles, touched.roles),
  });
  const before = pick(current);
  const after = pick(next);
  const yamlOf = (config: { sources: Item[]; roles: Item[] }) =>
    config.sources.length + config.roles.length === 0
      ? ""
      : stringify({
          ...(config.sources.length ? { sources: config.sources } : {}),
          ...(config.roles.length ? { roles: config.roles } : {}),
        });
  return {
    before: describeGraphjinConfig(before),
    after: describeGraphjinConfig(after),
    yaml: { before: yamlOf(before), after: yamlOf(after) },
  };
}
