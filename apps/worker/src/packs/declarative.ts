import { resolveWatcherVariables } from "@neko/llm/workflows";
import { Kind, parse, print, visit, type FieldNode, type SelectionSetNode } from "graphql";
import { basename, extname } from "node:path";
import { connectorActionSchema, sha256, type SolutionPackBundle } from "@neko/packs";

/** Resolve values, never interpolate secrets into serialized YAML or JSON. */
export function packValue(value: unknown, inputs: Record<string, unknown>, secrets: Record<string, string> = {}): unknown {
  if (typeof value === "string") {
    const match = /^\{\{([^}]+)}}$/.exec(value);
    if (!match) {
      if (value.includes("{{")) throw new Error("pack templates must occupy the complete value");
      return value;
    }
    const key = match[1]!.trim();
    const resolved = key.startsWith("secret.") ? secrets[key.slice(7)] : inputs[key];
    if (resolved === undefined) throw new Error(`missing pack template input ${key}`);
    return resolved;
  }
  if (Array.isArray(value)) return value.map(item => packValue(item, inputs, secrets));
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, packValue(item, inputs, secrets)]));
  return value;
}

/** Custom connectors use the existing GraphJin source/spec contract. */
export function declarativeGraphjinUpdate(
  bundle: SolutionPackBundle,
  inputs: Record<string, unknown>,
  secrets: Record<string, string>,
  retiredSources: string[] = [],
  bindings: Record<string, string> = {},
): Record<string, unknown> {
  for (const artifact of bundle.artifacts.filter(value => value.kind === "action")) {
    const action = connectorActionSchema.safeParse(artifact.content);
    if (!action.success) throw new Error("custom pack write actions require a supported action adapter");
    const connector = bundle.manifest.connectors?.find(value => value.id === action.data.adapter.connector);
    if (!action.data.kind.startsWith(`pack.${bundle.manifest.metadata.id}.`) || action.data.targetRef !== action.data.kind || !connector?.operations.some(value => value.id === action.data.adapter.operation)) throw new Error("Pack action must reference its own declared connector operation");
  }
  const knownChecks = new Set(["db-connect", "db-read-only", "graphjin-reload", "analytics-smoke", "queries"]);
  for (const check of [...bundle.manifest.health.requiredPreflight, ...bundle.manifest.health.postInstall, ...bundle.manifest.health.postWriteCanary, ...Object.values(bundle.manifest.health.readiness).flat()]) {
    if (["db-connect", "analytics-smoke", "queries"].includes(check) && !bundle.artifacts.some(artifact => artifact.kind === "saved_query")) throw new Error(`${check} requires a saved query`);
    if (!bundle.manifest.artifacts.graphjin) throw new Error(`${check} requires GraphJin artifacts`);
    if (!knownChecks.has(check)) throw new Error(`unsupported pack readiness check ${check}`);
    if (check.startsWith("db-") && !bundle.artifacts.some(artifact => artifact.kind === "source" && (artifact.content as Record<string, unknown>).kind === "database")) throw new Error(`${check} requires a database source`);
  }
  for (const artifact of bundle.artifacts) {
    if (artifact.kind === "metric" || artifact.kind === "watcher") {
      const value = artifact.content as Record<string, unknown>;
      packVariables(artifact.kind === "metric" ? (value.execution as Record<string, unknown>).variables : value.variables, inputs);
      for (const signal of (value.readinessSignals ?? []) as string[]) {
        if (!Object.hasOwn(bundle.manifest.health.readiness, signal)) throw new Error(`unknown pack readiness signal ${signal}`);
      }
    }
    if (artifact.kind === "workflow") {
      const schedule = (artifact.content as Record<string, unknown>).schedule as Record<string, unknown> | null;
      if (schedule) {
        const timezone = inputs[String(schedule.timezoneInput)] ?? schedule.timezoneInput;
        try { new Intl.DateTimeFormat("en", { timeZone: String(timezone) }).format(); }
        catch { throw new Error(`workflow ${artifact.key} timezone must resolve to an IANA timezone`); }
      }
    }
    if (artifact.kind === "saved_query") {
      readQuery(String(artifact.content));
    }
    if (artifact.kind === "spec") {
      const inspect = (value: unknown): void => {
        if (!value || typeof value !== "object") return;
        for (const [key, child] of Object.entries(value)) {
          if (key === "$ref" && (typeof child !== "string" || !child.startsWith("#/"))) throw new Error("pack OpenAPI references must be local");
          inspect(child);
        }
      };
      inspect(artifact.content);
    }
  }
  const sources = bundle.artifacts.filter(artifact => artifact.kind === "source").flatMap<Record<string, unknown>>(artifact => {
    const authored = artifact.content as Record<string, unknown>;
    const allowed = new Set(["name", "kind", "type", "host", "port", "dbname", "user", "password", "base_url", "openapi", "auth", "read_only"]);
    for (const key of Object.keys(authored)) {
      if (!allowed.has(key)) throw new Error(`unsupported custom source property ${key}`);
    }
    if (authored.read_only === false) throw new Error("custom pack sources must be read-only");
    if (bindings[artifact.key]) {
      if (Object.keys(authored).some(key => !["name", "kind", "read_only"].includes(key))) throw new Error("a source binding cannot also declare connection settings");
      return [];
    }
    const authoredAuth = authored.auth as Record<string, unknown> | undefined;
    for (const secret of [authored.password, authoredAuth?.token]) {
      if (secret !== undefined && (typeof secret !== "string" || !/^\{\{secret\.[a-z][a-z0-9_.-]+}}$/.test(secret))) {
        throw new Error("pack credentials must use declared secret references");
      }
    }
    const source = packValue(authored, inputs, secrets) as Record<string, unknown>;
    const common = {
      name: source.name, kind: source.kind, default: false, read_only: true,
      access: { read: "authenticated", write: "blocked", delete: "blocked" },
    };
    if (source.kind === "database") {
      if (!source.host || !source.dbname || !source.type) throw new Error("database source requires an explicit connection; select an existing-source binding for a source without connection settings");
      if (!["postgres", "mysql", "mariadb"].includes(String(source.type))) throw new Error("unsupported database type");
      return { ...source, ...common, capabilities: { "data.read": true, "data.write": false, "schema.read": true, "schema.write": false } };
    }
    const spec = bundle.artifacts.find(artifact => artifact.kind === "spec" && artifact.path === source.openapi);
    if (!spec) throw new Error(`API source ${source.name} must reference a bundled OpenAPI spec`);
    const url = new URL(String(source.base_url));
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new Error("API base URL must be HTTP(S) without credentials");
    const auth = source.auth as Record<string, unknown> | undefined;
    if (auth && (auth.type !== "bearer" || typeof auth.token !== "string" || Object.keys(auth).some(key => !["type", "token"].includes(key)))) throw new Error("custom API sources support bearer authentication only");
    return {
      ...common, specs_dir: "/config/specs",
      specs: { [basename(spec.path, extname(spec.path))]: {
        base_url: url.toString().replace(/\/$/, ""),
        ...(auth ? { auth: { scheme: "bearer", token: auth.token } } : {}),
      } },
      capabilities: { "api.read": true, "api.write": false, "api.delete": false },
    };
  });
  const names = Object.fromEntries(bundle.artifacts.filter(artifact => artifact.kind === "source" && bindings[artifact.key]).map(artifact => [String((artifact.content as Record<string, unknown>).name), bindings[artifact.key]]));
  const relationships = bundle.artifacts.filter(artifact => artifact.kind === "relationships").flatMap(artifact => {
    const value = artifact.content as { source: string; relationships: Array<{ left: string; right: string }> };
    return value.relationships.map(edge => ({ from: `${names[value.source] ?? value.source}:${edge.left}`, to: `${names[value.source] ?? value.source}:${edge.right}` }));
  });
  return {
    update_sources: sources, relationships,
    ...(retiredSources.length ? { source_patches: retiredSources.map(name => ({ name, read_only: true, access: { read: "blocked", write: "blocked", delete: "blocked" } })) } : {}),
  };
}

/** Existing generated packs use these time-window variables; authored declarations override them. */
export function packVariables(value: unknown, inputs: Record<string, unknown>): Record<string, unknown> {
  const definition = packValue(value ?? {
    from: { kind: "seconds_ago", seconds: 30 * 86400 }, to: { kind: "now" }, now: { kind: "now" },
    staleBefore: { kind: "seconds_ago", seconds: 86400 }, olderThan: { kind: "seconds_ago", seconds: 2 * 86400 }, threshold: { kind: "literal", value: 0 },
  }, inputs) as Record<string, unknown>;
  resolveWatcherVariables(definition, new Date());
  return definition;
}

function readQuery(query: string) {
  const document = parse(query);
  const operations = document.definitions.filter(value => value.kind === Kind.OPERATION_DEFINITION);
  if (operations.length !== 1 || operations[0]!.operation !== "query" || document.definitions.some(value => value.kind !== Kind.OPERATION_DEFINITION && value.kind !== Kind.FRAGMENT_DEFINITION)) {
    throw new Error("custom pack saved queries must contain one read-only operation");
  }
  return document;
}

/** GraphJin routes roots by table mappings; @database alone does not select the connection.
 * Pack-owned aliases preserve response names without changing administrator table mappings. */
export function bindPackQueries(bundle: SolutionPackBundle, bindings: Record<string, string>): { bundle: SolutionPackBundle; tables: Array<Record<string, string>> } {
  const tables: Array<Record<string, string>> = [];
  const sources = bundle.artifacts.filter(value => value.kind === "source");
  const databases = sources.filter(value => (value.content as Record<string, unknown>).kind === "database");
  if (!databases.length) return { bundle, tables };
  const names = Object.fromEntries(databases.map(value => {
    const name = String((value.content as Record<string, unknown>).name);
    return [name, bindings[value.key] ?? name];
  }));
  const defaultDatabase = sources.length === 1 ? Object.keys(names)[0] : undefined;
  const resolved = { ...bundle, artifacts: bundle.artifacts.map(artifact => {
    if (artifact.kind !== "saved_query") return artifact;
    const document = readQuery(String(artifact.content));
    const roots = new Set<FieldNode>();
    const collect = (set: SelectionSetNode, seen = new Set<string>()): void => {
      for (const selection of set.selections) {
        if (selection.kind === Kind.FIELD) roots.add(selection);
        else if (selection.kind === Kind.INLINE_FRAGMENT) collect(selection.selectionSet, seen);
        else {
          const name = selection.name.value;
          const fragment = document.definitions.find(value => value.kind === Kind.FRAGMENT_DEFINITION && value.name.value === name);
          if (!fragment || fragment.kind !== Kind.FRAGMENT_DEFINITION || seen.has(name)) throw new Error("invalid saved query fragment");
          collect(fragment.selectionSet, new Set([...seen, name]));
        }
      }
    };
    for (const definition of document.definitions) if (definition.kind === Kind.OPERATION_DEFINITION) collect(definition.selectionSet);
    const bound = visit(document, { Field(node) {
      const directives = node.directives ?? [];
      const database = directives.filter(value => value.name.value === "database");
      if (database.length > 1) throw new Error("duplicate database directive");
      if (!database.length && !roots.has(node)) return;
      const argument = database[0]?.arguments?.find(value => value.name.value === "name")?.value;
      const logical = argument?.kind === Kind.STRING || argument?.kind === Kind.ENUM ? argument.value : !database.length ? defaultDatabase : undefined;
      if (!logical || !names[logical]) throw new Error(`saved query ${artifact.key} must select a declared database with @database(name: "source_name")`);
      const table = node.name.value;
      const name = `pack_${sha256(`${bundle.manifest.metadata.id}:${logical}:${names[logical]}:${table}`).slice(0, 20)}`;
      if (roots.has(node) && !tables.some(value => value.name === name)) tables.push({ name, table, source: names[logical]! });
      return { ...node, ...(roots.has(node) ? { alias: node.alias ?? node.name, name: { kind: Kind.NAME, value: name } } : {}), directives: [...directives.filter(value => value.name.value !== "database"), {
        kind: Kind.DIRECTIVE, name: { kind: Kind.NAME, value: "database" },
        arguments: [{ kind: Kind.ARGUMENT, name: { kind: Kind.NAME, value: "name" }, value: { kind: Kind.STRING, value: names[logical] } }],
      }] };
    } });
    return { ...artifact, content: print(bound) };
  }) };
  return { bundle: resolved, tables };
}
