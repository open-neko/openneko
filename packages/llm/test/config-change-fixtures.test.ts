import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  assertDatabaseSourcesStayReadOnly,
  buildGraphjinConfigUpdate,
  graphjinConfigPatchHash,
  graphjinInputWithJsonVariables,
} from "../src/graphjin/config-change";
import { describeGraphjinConfigChange } from "../src/graphjin/config-describe";
import {
  checkSourceConfigAnswers,
  describeConfigProblems,
  mapGraphjinConfigErrors,
} from "../src/graphjin/config-problems";
import { managedFileSourceRuntimeRoot } from "../src/graphjin/file-source";
import { parseManagedFileSourceManifest } from "../src/graphjin/file-source-assets";
import { mergeGraphjinSourceConfigUpdate } from "../src/graphjin/persist-source-config";

// The Go preview of request_source_config_change
// (apps/openneko/internal/webfront/configchange.go) must give the same
// results. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/configchange.json");

const SUM = "a".repeat(64);
const MANIFEST = {
  sourceName: "docs",
  ready: true,
  files: [{ name: "a.pdf", size: 10, contentType: "application/pdf", checksumSha256: SUM }],
  totalSize: 10,
};

const OPTIONS = { managedOpenApiSpecsDir: "/config/specs", managedLocalFilesRoot: "/config/files/org_x_docs" };

const UPDATES: Array<{ name: string; payload: Record<string, unknown>; options?: typeof OPTIONS }> = [
  { name: "role", payload: { action: "add_role", name: " analyst ", match: " user.role == 'analyst' ", comment: " note " } },
  { name: "role no match", payload: { action: "add_role", name: "analyst" } },
  { name: "access", payload: { action: "set_source_access", source: " erp ", read: "admin", write: "blocked" } },
  { name: "access empty", payload: { action: "set_source_access", source: "erp" } },
  { name: "access no source", payload: { action: "set_source_access", read: "admin" } },
  { name: "tables", payload: { action: "set_source_tables", source: "erp", blockedTables: ["payroll", " Payroll ", "hr.salary"], adminTables: ["audit"], openTables: ["orders"] } },
  { name: "tables overlap", payload: { action: "set_source_tables", source: "erp", blockedTables: ["payroll"], openTables: ["PAYROLL"] } },
  { name: "tables bad name", payload: { action: "set_source_tables", source: "erp", blockedTables: ["drop table"] } },
  { name: "tables not a list", payload: { action: "set_source_tables", source: "erp", adminTables: "payroll" } },
  { name: "tables none", payload: { action: "set_source_tables", source: "erp", blockedTables: [] } },
  { name: "capabilities", payload: { action: "set_source_capabilities", source: "shop", apiWrite: true, apiDelete: false } },
  { name: "capabilities none", payload: { action: "set_source_capabilities", source: "shop" } },
  { name: "expose", payload: { action: "expose_api_operation", source: "shop", spec: "orders", operation: "createOrder", exposeMutation: true, allowedRoles: [" Admin ", "member", ""], exposeAs: " create_order " } },
  { name: "expose no roles", payload: { action: "expose_api_operation", source: "shop", spec: "orders", operation: "createOrder", exposeMutation: true } },
  { name: "expose off", payload: { action: "expose_api_operation", source: "shop", spec: "orders", operation: "createOrder" } },
  { name: "expose missing", payload: { action: "expose_api_operation", source: "shop" } },
  { name: "api writes", payload: { action: "enable_api_writes", source: "shop", spec: "orders", operation: "createOrder", allowedRoles: ["admin"], write: "admin", apiDelete: true, exposeAs: "x" } },
  { name: "api writes default", payload: { action: "enable_api_writes", source: "shop", spec: "orders", operation: "createOrder", allowedRoles: ["member"] } },
  { name: "api writes bad mode", payload: { action: "enable_api_writes", source: "shop", spec: "orders", operation: "createOrder", allowedRoles: ["member"], write: "owner" } },
  { name: "api writes no roles", payload: { action: "enable_api_writes", source: "shop", spec: "orders", operation: "createOrder", allowedRoles: [] } },
  { name: "database", payload: { action: "register_source", name: " erp ", kind: ["database"], type: ["MySQL"], host: " db.internal ", port: 3306, dbname: "erp", user: "reader", secretRef: " erp-password ", blockedTables: ["payroll"], adminTables: ["audit"] } },
  { name: "database defaults", payload: { action: "register_source", name: "erp", host: "db", dbname: "erp", user: "u", secretRef: "erp-password", read: "admin" } },
  { name: "database no secret", payload: { action: "register_source", name: "erp", kind: "database", host: "db" } },
  { name: "database empty secret", payload: { action: "register_source", name: "erp", kind: "database", secretRef: "empty-secret" } },
  { name: "database unknown secret", payload: { action: "register_source", name: "erp", kind: "database", secretRef: "missing" } },
  { name: "database bad type", payload: { action: "register_source", name: "erp", type: "oracle", secretRef: "erp-password" } },
  { name: "database overlap", payload: { action: "register_source", name: "erp", blockedTables: ["a"], adminTables: ["A"], secretRef: "erp-password" } },
  { name: "no name", payload: { action: "register_source", kind: "database" } },
  { name: "bad kind", payload: { action: "register_source", name: "x", kind: "sheet" } },
  { name: "api", payload: { action: "register_source", name: "shop", kind: "api", specAssetId: " 88888888-8888-4888-8888-888888888888 " }, options: OPTIONS },
  { name: "api default dir", payload: { action: "register_source", name: "shop", kind: "api", specAssetId: "88888888-8888-4888-8888-888888888888" } },
  { name: "api no asset", payload: { action: "register_source", name: "shop", kind: "api" } },
  { name: "file local", payload: { action: "register_source", name: "docs", kind: "file", backend: ["local"], localFiles: MANIFEST }, options: OPTIONS },
  { name: "file local no root", payload: { action: "register_source", name: "docs", kind: "file", backend: "local", localFiles: MANIFEST } },
  { name: "file local wrong manifest", payload: { action: "register_source", name: "docs", kind: "file", backend: "local", localFiles: { ...MANIFEST, sourceName: "other" } }, options: OPTIONS },
  { name: "file root given", payload: { action: "register_source", name: "docs", kind: "file", backend: "local", root: "/etc" }, options: OPTIONS },
  { name: "file bad name", payload: { action: "register_source", name: "my-docs", kind: "file", backend: "s3" } },
  { name: "file no backend", payload: { action: "register_source", name: "docs", kind: "file" } },
  { name: "file s3", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "my.bucket-1", prefix: "a/b/", region: "eu-west-1", endpoint: "https://s3.example.com/", publicBaseUrl: "https://cdn.example.com/files/", presignTtl: "30m" } },
  { name: "file gcs", payload: { action: "register_source", name: "docs", kind: "file", backend: "gcs", bucket: "my_bucket", region: "ignored", presignTtl: "1h" } },
  { name: "file gcs endpoint", payload: { action: "register_source", name: "docs", kind: "file", backend: "gcs", bucket: "my_bucket", endpoint: "https://x" } },
  { name: "file s3 no bucket", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3" } },
  { name: "file s3 bad bucket", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "1.2.3.4" } },
  { name: "file gcs goog bucket", payload: { action: "register_source", name: "docs", kind: "file", backend: "gcs", bucket: "google-x" } },
  { name: "file absolute prefix", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", prefix: "/a" } },
  { name: "file endpoint ftp", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", endpoint: "ftp://x" } },
  { name: "file endpoint query", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", endpoint: "https://x/?a=1" } },
  { name: "file endpoint user", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", endpoint: "https://u:p@x" } },
  { name: "file endpoint invalid", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", endpoint: "not a url" } },
  { name: "file public http", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", publicBaseUrl: "http://cdn" } },
  { name: "file ttl short", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", presignTtl: "30s" } },
  { name: "file ttl long", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", presignTtl: "2h" } },
  { name: "file ttl bad", payload: { action: "register_source", name: "docs", kind: "file", backend: "s3", bucket: "abc", presignTtl: "15 minutes" } },
  { name: "unknown action", payload: { action: "drop_source" } },
];

const CURRENT = [
  { name: "erp", kind: "database", type: "postgres", host: "db", password: "[REDACTED]", read_only: true, access: { read: "account", blocked_tables: ["payroll"] } },
  { name: "shop", kind: "api", read_only: true, specs: { orders: {} } },
];

const READ_ONLY: Array<{ name: string; update: Record<string, unknown>; current?: unknown }> = [
  { name: "api writes on api", update: { update_sources: [{ name: "shop", read_only: false, capabilities: { "api.write": true } }] }, current: CURRENT },
  { name: "writes on database", update: { update_sources: [{ name: "ERP", read_only: false, capabilities: { "api.write": true, "api.delete": true }, specs: {} }], source_patches: [{ name: "erp", access: { write: "authenticated", delete: "blocked" } }] }, current: CURRENT },
  { name: "unknown source counts as database", update: { source_patches: [{ name: "new", access: { delete: "admin" } }] }, current: CURRENT },
  { name: "file kind is writable", update: { update_sources: [{ name: "docs", kind: "file", read_only: false }] } },
  { name: "blocked writes pass", update: { source_patches: [{ name: "erp", access: { write: "blocked", read: "admin" } }] }, current: CURRENT },
  { name: "no current", update: { update_sources: [{ name: "x", read_only: false }] }, current: "not a list" },
];

const INPUTS: unknown[] = [
  { mode: "preview", expected_catalog_revision: "r1", roles: [{ name: "a", match: "x" }] },
  { update_sources: [{ name: "shop", capabilities: { "api.write": true }, specs: { orders: { operations: { createOrder: { expose_mutation: true, allowed_roles: ["admin"] } } } } }], source_patches: [{ name: "shop", access: { write: "admin" } }] },
  { a: null, b: [1, 2.5, true, "q\"uote"], c: { "my-key": { x: 1 }, ok: { "1x": 2 } } },
];

const ANSWERS: Array<{ name: string; payload: Record<string, unknown>; context?: Record<string, unknown> }> = [
  { name: "role empty", payload: { action: "add_role", name: " ", match: "" } },
  { name: "role ok", payload: { action: "add_role", name: "a", match: "b" } },
  { name: "register empty", payload: { action: "register_source" }, context: { secretNames: ["erp-password"] } },
  { name: "register taken", payload: { action: "register_source", name: "ERP", host: "db", port: "70000", dbname: "erp", user: "u", secretRef: "nope" }, context: { secretNames: ["erp-password"], currentSources: JSON.stringify(CURRENT) } },
  { name: "register port text", payload: { action: "register_source", name: "x", host: "db", port: "abc", dbname: "d", user: "u", secretRef: "erp-password" } },
  { name: "register port ok", payload: { action: "register_source", name: "x", host: "db", port: 5432, dbname: "d", user: "u", secretRef: "s" } },
  { name: "register port empty", payload: { action: "register_source", name: ["x"], host: "db", port: "", dbname: "d", user: "u", secretRef: "s" }, context: { secretNames: [] } },
  { name: "register api", payload: { action: "register_source", name: "x", kind: ["api"] } },
  { name: "source missing", payload: { action: "set_source_access" } },
  { name: "source unknown", payload: { action: "set_source_access", source: "crm" }, context: { currentSources: CURRENT } },
  { name: "source unknown no list", payload: { action: "set_source_access", source: "crm" } },
  { name: "tables unknown", payload: { action: "set_source_tables", source: "ERP", blockedTables: ["orders", "nope", "PAYROLL"], adminTables: ["x", "y"], openTables: [" "] }, context: { currentSources: CURRENT, sourceTables: [["erp", ["orders", "Customers"]]] } },
  { name: "tables no catalog", payload: { action: "set_source_tables", source: "erp", blockedTables: ["nope"] }, context: { currentSources: CURRENT } },
];

const GJ_ERRORS: Array<{ name: string; errors: unknown; payload: Record<string, unknown> }> = [
  { name: "host", errors: ["database 'erp' (reader@db.internal:5432/erp): connection failed: dial tcp: lookup db.internal: no such host"], payload: { action: "register_source" } },
  { name: "port", errors: [{ message: "database 'erp' (reader@db:5433/erp): connection failed: connect: connection refused" }], payload: { action: "register_source" } },
  { name: "password", errors: "database 'erp' (reader@db:5432/erp): connection failed: FATAL: password authentication failed for user \"reader\"", payload: { action: "register_source" } },
  { name: "role", errors: ["database 'erp' (bob@db:5432/erp): connection failed: FATAL: role \"bob\" does not exist", "database 'erp' (u@db:5432/x): connection failed: FATAL: database \"x\" does not exist"], payload: {} },
  { name: "connect right", errors: ["database 'erp' (u@db:5432/x): connection failed: permission denied for database x", "database 'erp' (u@db:5432/x): connection failed: x509: certificate signed by unknown authority", "database 'erp' (u@db:5432/x): connection failed: something\nelse"], payload: {} },
  { name: "access", errors: ["sources[\"erp\"].access.read: unknown mode \"x\""], payload: { action: "set_source_access" } },
  { name: "names", errors: ["sources[0]: name is required", "duplicate source names: erp", "unsupported database type \"oracle\"", "cannot use System Database", "other", "", 5, { nope: 1 }], payload: { action: "register_source" } },
  { name: "names other action", errors: ["sources[1]: name is required", "source erp already exists"], payload: { action: "set_source_tables" } },
  { name: "not a list", errors: { message: "x" }, payload: {} },
];

const CONFIG_YAML = `# GraphJin config
app_name: test
sources:
  - name: erp
    kind: database
    type: postgres
    host: db
    password: \${ERP_PASSWORD}
    read_only: true
    access:
      read: account
      blocked_tables: [payroll, audit]
      admin_tables:
        - salaries
roles:
  - name: user
    match: id IS NOT NULL
`;

const MERGES: Array<{ name: string; raw: string; update: Record<string, unknown> }> = [
  { name: "new database source", raw: CONFIG_YAML, update: { update_sources: [{ name: "crm", kind: "database", read_only: true, type: "mysql", host: "h", port: 3306, user: "u", password: "pw-planted", access: { read: "authenticated", write: "blocked", delete: "blocked", blocked_tables: ["x"] } }] } },
  { name: "existing source merge", raw: CONFIG_YAML, update: { update_sources: [{ name: "erp", capabilities: { "api.write": true }, access: { read: "admin" }, headers: { Authorization: "Bearer x", "X-Plain": "y" } }] } },
  { name: "table patch", raw: CONFIG_YAML, update: { source_patches: [{ name: "erp", access: { blocked_tables_add: ["Salaries", "orders"], admin_tables_add: ["payroll"], blocked_tables_remove: ["AUDIT"], admin_tables_remove: ["orders"] } }] } },
  { name: "open tables", raw: CONFIG_YAML, update: { source_patches: [{ name: "erp", access: { blocked_tables_remove: ["payroll"], admin_tables_remove: ["payroll"], write: "blocked" } }] } },
  { name: "add and remove one table", raw: CONFIG_YAML, update: { source_patches: [{ name: "erp", access: { blocked_tables_add: ["orders"], blocked_tables_remove: ["orders", "payroll"] } }] } },
  { name: "patch absent source", raw: CONFIG_YAML, update: { source_patches: [{ name: "crm", access: { read: "admin" } }] } },
  { name: "roles", raw: CONFIG_YAML, update: { roles: [{ name: "USER", match: "x" }, { name: "analyst", match: "y", comment: "z" }] } },
  { name: "remove", raw: CONFIG_YAML, update: { remove_sources: ["ERP"] } },
  { name: "no sources yet", raw: "app_name: x\n", update: { update_sources: [{ name: "a", kind: "api", specs_dir: "/config/specs", secret_token: "gjsecret://keep", api_key: "${KEEP}" }] } },
  { name: "no sources and no change", raw: "app_name: x\n", update: { roles: [{ name: "a", match: "b" }] } },
  { name: "flow sources", raw: "sources: []\n", update: { update_sources: [{ name: "a/b", kind: "file", list: [{ name: "n", token: "t" }, { password: "p" }] }] } },
  { name: "bad yaml", raw: "a: [\n", update: {} },
  { name: "root list", raw: "- a\n", update: {} },
  { name: "sources map", raw: "sources: {}\n", update: {} },
  { name: "no name", raw: CONFIG_YAML, update: { update_sources: [{ kind: "api" }] } },
  { name: "invalid source", raw: CONFIG_YAML, update: { update_sources: ["x"] } },
];

const DESCRIBES: Array<{ name: string; current: { sources?: unknown; roles?: unknown }; update: Record<string, unknown> }> = [
  { name: "new database", current: { sources: JSON.stringify(CURRENT), roles: "[{\"Name\":\"user\",\"Match\":\"id IS NOT NULL\"}]" }, update: { update_sources: [{ name: "crm", kind: "database", read_only: true, type: "mysql", host: "h", port: 3306, dbname: "c", user: "u", password: "pw-planted", access: { read: "authenticated", write: "blocked", delete: "blocked", blocked_tables: ["x"] } }] } },
  { name: "table patch", current: { sources: CURRENT, roles: [] }, update: { source_patches: [{ name: "erp", access: { blocked_tables_add: ["orders"], admin_tables_add: ["payroll"] } }] } },
  { name: "api writes", current: { sources: CURRENT }, update: { update_sources: [{ name: "shop", read_only: false, capabilities: { "api.write": true }, specs: { orders: { operations: { createOrder: { expose_mutation: true, allowed_roles: ["admin"] } } } } }], source_patches: [{ name: "shop", access: { write: "admin" } }] } },
  { name: "role", current: { roles: [{ name: "user", match: "a" }] }, update: { roles: [{ name: "analyst", match: "b", comment: "c" }] } },
  { name: "role replaced", current: { roles: [{ Name: "user", Match: "a" }] }, update: { roles: [{ name: "USER", match: "b" }] } },
  { name: "empty", current: {}, update: {} },
];

const MANIFESTS: Array<{ name: string; value: unknown; sourceName: string }> = [
  { name: "ok", value: { ...MANIFEST, files: [...MANIFEST.files, { name: "b c (1).txt", size: 5, contentType: "text/plain", checksumSha256: SUM }], totalSize: 15 }, sourceName: "docs" },
  { name: "bad source name", value: MANIFEST, sourceName: "1docs" },
  { name: "not an object", value: [MANIFEST], sourceName: "docs" },
  { name: "not ready", value: { ...MANIFEST, ready: "yes" }, sourceName: "docs" },
  { name: "no files", value: { ...MANIFEST, files: [] }, sourceName: "docs" },
  { name: "bad entry", value: { ...MANIFEST, files: [null] }, sourceName: "docs" },
  { name: "path name", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], name: "../a" }] }, sourceName: "docs" },
  { name: "trailing dot", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], name: "a." }] }, sourceName: "docs" },
  { name: "duplicate", value: { ...MANIFEST, files: [MANIFEST.files[0], MANIFEST.files[0]], totalSize: 20 }, sourceName: "docs" },
  { name: "size", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], size: 1.5 }] }, sourceName: "docs" },
  { name: "too big", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], size: 30 * 1024 * 1024 }] }, sourceName: "docs" },
  { name: "checksum", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], checksumSha256: "A".repeat(64) }] }, sourceName: "docs" },
  { name: "content type", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], contentType: " text/plain" }] }, sourceName: "docs" },
  { name: "empty content type", value: { ...MANIFEST, files: [{ ...MANIFEST.files[0], contentType: "" }] }, sourceName: "docs" },
  { name: "total", value: { ...MANIFEST, totalSize: 11 }, sourceName: "docs" },
];

function outcome<T>(run: () => T): { value: T } | { error: string } {
  try {
    return { value: run() };
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

async function resolveSecret(name: string): Promise<string> {
  if (name === "erp-password") return "pw-planted";
  if (name === "empty-secret") return "";
  throw new Error(`no stored source secret named "${name}"; add it in GraphJin Config first`);
}

describe("config change fixtures for the Go preview", () => {
  it("matches the fixture the Go test reads", async () => {
    const updates = [];
    for (const item of UPDATES) {
      try {
        const built = await buildGraphjinConfigUpdate(item.payload, resolveSecret, item.options);
        updates.push({ ...item, update: built.update, secretName: built.secretName });
      } catch (err) {
        updates.push({ ...item, error: (err as Error).message, errorName: (err as Error).name });
      }
    }
    const readOnly = READ_ONLY.map((item) => ({ ...item, ...outcome(() => assertDatabaseSourcesStayReadOnly(item.update, item.current)) }));
    const inputs = INPUTS.map((value) => ({ value, ...graphjinInputWithJsonVariables(value) }));
    const hashes = UPDATES.map((item) => ({ payload: item.payload, hash: graphjinConfigPatchHash(item.payload) }));
    const answers = ANSWERS.map((item) => {
      const context = item.context
        ? { ...item.context, sourceTables: item.context.sourceTables ? new Map(item.context.sourceTables as Array<[string, string[]]>) : undefined }
        : undefined;
      const problems = checkSourceConfigAnswers(item.payload, context);
      return { ...item, problems, text: describeConfigProblems(problems) };
    });
    const gjErrors = GJ_ERRORS.map((item) => {
      const problems = mapGraphjinConfigErrors(item.errors, item.payload);
      return { ...item, problems, text: describeConfigProblems(problems) };
    });
    const merges = MERGES.map((item) => ({ ...item, ...outcome(() => mergeGraphjinSourceConfigUpdate(item.raw, item.update)) }));
    const describes = DESCRIBES.map((item) => ({ ...item, ...outcome(() => describeGraphjinConfigChange(item)) }));
    const manifests = MANIFESTS.map((item) => ({ ...item, ...outcome(() => parseManagedFileSourceManifest(item.value, item.sourceName)) }));
    const roots = [
      { orgId: "11111111-1111-4111-8111-111111111111", sourceName: "docs", runtimeBase: "/config/files//" },
      { orgId: "o", sourceName: "docs", runtimeBase: "relative" },
      { orgId: "o", sourceName: "bad-name", runtimeBase: "/x" },
    ].map((item) => ({ ...item, ...outcome(() => managedFileSourceRuntimeRoot(item)) }));
    const fixture = `${JSON.stringify({ updates, readOnly, inputs, hashes, answers, gjErrors, merges, describes, manifests, roots }, null, 1)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
