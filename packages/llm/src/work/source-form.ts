import { z } from "zod";
import type { AgentSurfaceMessage } from "../agent-backend";
import { A2UI_CATALOG_ID, A2UI_VERSION } from "./a2ui-contract";
import { DATABASE_SOURCE_TYPES } from "../graphjin/source-types";
import type { ConfigProblem } from "../graphjin/config-problems";

const optionalText = (max: number) => z.string().trim().max(max).optional();

/** Optional values the agent already knows. The admin completes the rest. */
export const SOURCE_FORM_PREFILL_SHAPE = {
  name: optionalText(64),
  kind: z.enum(["database", "api", "file"]).optional(),
  type: z.enum(DATABASE_SOURCE_TYPES).optional(),
  host: optionalText(255),
  port: z.number().int().min(1).max(65_535).optional(),
  dbname: optionalText(128),
  user: optionalText(128),
  secretRef: optionalText(128).describe("A stored secret name from list_source_secret_names. Leave it out to let the admin type the password in the card."),
  blockedTables: optionalText(2_000).describe("Comma-separated tables that no one may read."),
  adminTables: optionalText(2_000).describe("Comma-separated tables that only admins may read."),
  backend: z.enum(["local", "s3", "gcs"]).optional(),
  bucket: optionalText(255),
  prefix: optionalText(255),
  region: optionalText(64),
  endpoint: optionalText(255),
  publicBaseUrl: optionalText(255),
  presignTtl: optionalText(16),
};

const sourceFormPrefillSchema = z.object(SOURCE_FORM_PREFILL_SHAPE);

const FORM_FIELDS = new Set(["name", "kind", "type", "host", "port", "dbname", "user", "secretRef", "blockedTables", "adminTables", "backend", "bucket", "prefix", "region", "endpoint", "publicBaseUrl", "presignTtl"]);

function problemsByField(problems: readonly ConfigProblem[]): Record<string, string> {
  const out: Record<string, string> = {};
  const general: string[] = [];
  for (const problem of problems) {
    if (problem.field && FORM_FIELDS.has(problem.field)) {
      out[problem.field] = out[problem.field] ? `${out[problem.field]} ${problem.message}` : problem.message;
    } else general.push(problem.message);
  }
  if (general.length > 0) out.general = general.join(" ");
  return out;
}
export type SourceFormPrefill = z.infer<typeof sourceFormPrefillSchema>;

const SOURCE_FORM_COMPONENTS: Array<Record<string, unknown>> = [
  {"id": "root", "component": "Answer", "eyebrow": "GRAPHJIN CONFIG", "title": "Add a source", "subtitle": "Choose a source type and provide its connection details.", "children": ["note", "problemsNote", "common", "databaseFields", "apiFields", "fileFields", "databaseSubmit", "apiSubmit", "fileSubmit"]},
  {"id": "note", "component": "Callout", "mood": "watch", "title": "Approval required", "text": "Submitting creates a configuration proposal for admin review."},
  {"id": "problemsNote", "component": "Conditional", "when": {"path": "/problems/general"}, "children": ["problemsCallout"]},
  {"id": "problemsCallout", "component": "Callout", "mood": "watch", "title": "Fix these answers", "text": {"path": "/problems/general"}},
  {"id": "common", "component": "Column", "children": ["name", "kind"]},
  {"id": "name", "component": "TextField", "label": "Source name", "value": {"path": "/form/name"}, "placeholder": "warehouse", "error": {"path": "/problems/name"}},
  {"id": "kind", "component": "ChoicePicker", "label": "Source kind", "options": [{"label": "Database", "value": "database"}, {"label": "API", "value": "api"}, {"label": "Files", "value": "file"}], "value": {"path": "/form/kind"}, "variant": "mutuallyExclusive", "error": {"path": "/problems/kind"}},
  {"id": "databaseFields", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "database", "children": ["type", "network", "database", "password", "connectionCheck", "tables"]},
  {"id": "type", "component": "ChoicePicker", "label": "Database type", "options": [{"label": "PostgreSQL", "value": "postgres"}, {"label": "MySQL", "value": "mysql"}, {"label": "MariaDB", "value": "mariadb"}], "value": {"path": "/form/type"}, "variant": "mutuallyExclusive", "error": {"path": "/problems/type"}},
  {"id": "network", "component": "Row", "children": ["host", "port"]},
  {"id": "host", "component": "TextField", "label": "Host", "value": {"path": "/form/host"}, "placeholder": "db.internal", "error": {"path": "/problems/host"}},
  {"id": "port", "component": "TextField", "label": "Port", "value": {"path": "/form/port"}, "variant": "number", "error": {"path": "/problems/port"}},
  {"id": "database", "component": "Row", "children": ["dbname", "user"]},
  {"id": "dbname", "component": "TextField", "label": "Database", "value": {"path": "/form/dbname"}, "error": {"path": "/problems/dbname"}},
  {"id": "user", "component": "TextField", "label": "Read-only login", "value": {"path": "/form/user"}, "error": {"path": "/problems/user"}},
  {"id": "password", "component": "SourceSecretInput", "label": "Password", "sourceName": {"path": "/form/name"}, "value": {"path": "/form/secretRef"}, "error": {"path": "/problems/secretRef"}},
  {"id": "connectionCheck", "component": "SourceConnectionCheck", "label": "Check connection", "type": {"path": "/form/type"}, "host": {"path": "/form/host"}, "port": {"path": "/form/port"}, "dbname": {"path": "/form/dbname"}, "user": {"path": "/form/user"}, "secretRef": {"path": "/form/secretRef"}, "value": {"path": "/form/blockedTables"}},
  {"id": "tables", "component": "Column", "children": ["blockedTables", "adminTables"]},
  {"id": "blockedTables", "component": "TextField", "label": "Tables no one may read (optional)", "value": {"path": "/form/blockedTables"}, "placeholder": "admin_user, oauth_token", "error": {"path": "/problems/blockedTables"}},
  {"id": "adminTables", "component": "TextField", "label": "Tables only admins may read (optional)", "value": {"path": "/form/adminTables"}, "placeholder": "payroll", "error": {"path": "/problems/adminTables"}},
  {"id": "apiFields", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "api", "children": ["openApiSpec"]},
  {"id": "openApiSpec", "component": "OpenApiSpecInput", "label": "OpenAPI specification", "value": {"path": "/form/openApiSpec"}},
  {"id": "fileFields", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "file", "children": ["fileSecurity", "backend", "localFields", "objectFields", "s3Fields"]},
  {"id": "fileSecurity", "component": "Callout", "mood": "watch", "title": "Read-only source", "text": "Authenticated organization members receive list/read access to approved file content."},
  {"id": "backend", "component": "ChoicePicker", "label": "Storage backend", "options": [{"label": "Local", "value": "local"}, {"label": "S3", "value": "s3"}, {"label": "GCS", "value": "gcs"}], "value": {"path": "/form/backend"}, "variant": "mutuallyExclusive", "error": {"path": "/problems/backend"}},
  {"id": "localFields", "component": "Conditional", "when": {"path": "/form/backend"}, "equals": "local", "children": ["managedFiles"]},
  {"id": "managedFiles", "component": "ManagedFileSourceInput", "label": "Managed local files", "sourceName": {"path": "/form/name"}, "value": {"path": "/form/localFiles"}},
  {"id": "objectFields", "component": "Conditional", "when": {"path": "/form/backend"}, "oneOf": ["s3", "gcs"], "children": ["cloudCredentials", "bucket", "prefix", "publicBaseUrl", "presignTtl"]},
  {"id": "cloudCredentials", "component": "Callout", "mood": "watch", "title": "Deployment credentials", "text": "GraphJin uses its AWS or Google Cloud runtime identity; chat and approval carry storage metadata."},
  {"id": "bucket", "component": "TextField", "label": "Bucket", "value": {"path": "/form/bucket"}, "error": {"path": "/problems/bucket"}},
  {"id": "prefix", "component": "TextField", "label": "Key prefix", "value": {"path": "/form/prefix"}, "error": {"path": "/problems/prefix"}},
  {"id": "publicBaseUrl", "component": "TextField", "label": "Public HTTPS base URL (optional)", "value": {"path": "/form/publicBaseUrl"}, "error": {"path": "/problems/publicBaseUrl"}},
  {"id": "presignTtl", "component": "TextField", "label": "Presigned URL TTL", "value": {"path": "/form/presignTtl"}, "placeholder": "15m", "error": {"path": "/problems/presignTtl"}},
  {"id": "s3Fields", "component": "Conditional", "when": {"path": "/form/backend"}, "equals": "s3", "children": ["region", "endpoint"]},
  {"id": "region", "component": "TextField", "label": "Region", "value": {"path": "/form/region"}, "error": {"path": "/problems/region"}},
  {"id": "endpoint", "component": "TextField", "label": "S3-compatible endpoint", "value": {"path": "/form/endpoint"}, "error": {"path": "/problems/endpoint"}},
  {"id": "submitLabel", "component": "Text", "text": "Review proposal"},
  {"id": "databaseSubmit", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "database", "children": ["submitDatabase"]},
  {"id": "apiSubmit", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "api", "children": ["submitApi"]},
  {"id": "fileSubmit", "component": "Conditional", "when": {"path": "/form/kind"}, "equals": "file", "children": ["localSubmit", "s3Submit", "gcsSubmit"]},
  {"id": "localSubmit", "component": "Conditional", "when": {"path": "/form/backend"}, "equals": "local", "children": ["submitLocalFile"]},
  {"id": "s3Submit", "component": "Conditional", "when": {"path": "/form/backend"}, "equals": "s3", "children": ["submitCloudFile"]},
  {"id": "gcsSubmit", "component": "Conditional", "when": {"path": "/form/backend"}, "equals": "gcs", "children": ["submitCloudFile"]},
  {"id": "submitDatabase", "component": "Button", "child": "submitLabel", "variant": "primary", "requires": [{"path": "/form/name"}, {"path": "/form/secretRef"}], "action": {"event": {"name": "submit_source_config", "context": {"prompt": "File the register_source database proposal from these values. Split blockedTables and adminTables at commas into lists. Use secretRef as given.", "values": {"path": "/form"}}}}},
  {"id": "submitApi", "component": "Button", "child": "submitLabel", "variant": "primary", "requires": [{"path": "/form/name"}, {"path": "/form/openApiSpec/id"}], "action": {"event": {"name": "submit_source_config", "context": {"prompt": "Use openApiSpec.id as specAssetId and file the register_source API configuration proposal.", "values": {"path": "/form"}}}}},
  {"id": "submitLocalFile", "component": "Button", "child": "submitLabel", "variant": "primary", "requires": [{"path": "/form/name"}, {"path": "/form/localFiles/sourceName"}], "action": {"event": {"name": "submit_source_config", "context": {"prompt": "Create the read-only local file source proposal using the managed localFiles manifest and selected source name.", "values": {"path": "/form"}}}}},
  {"id": "submitCloudFile", "component": "Button", "child": "submitLabel", "variant": "primary", "requires": [{"path": "/form/name"}, {"path": "/form/bucket"}], "action": {"event": {"name": "submit_source_config", "context": {"prompt": "Create the read-only cloud file source proposal using the selected backend, bucket, prefix, endpoint, public base URL, and bounded presign TTL values.", "values": {"path": "/form"}}}}},
];

/** Build the canonical source configuration form with the agent's prefill. */
export function buildSourceFormSurface(
  prefill: SourceFormPrefill,
  surfaceId: string,
  problems: readonly ConfigProblem[] = [],
): AgentSurfaceMessage[] {
  const { kind, backend, type, ...fields } = sourceFormPrefillSchema.parse(prefill);
  const form = {
    name: "",
    kind: [kind ?? "database"],
    type: [type ?? "postgres"],
    host: "",
    port: 5432,
    dbname: "",
    user: "",
    secretRef: "",
    blockedTables: "",
    adminTables: "",
    openApiSpec: null,
    backend: [backend ?? "local"],
    localFiles: null,
    bucket: "",
    prefix: "",
    region: "",
    endpoint: "",
    publicBaseUrl: "",
    presignTtl: "15m",
    ...Object.fromEntries(Object.entries(fields).filter(([, value]) => value !== undefined)),
  };
  return [{
    version: A2UI_VERSION,
    createSurface: {
      surfaceId,
      catalogId: A2UI_CATALOG_ID,
      dataModel: { form, problems: problemsByField(problems) },
      components: SOURCE_FORM_COMPONENTS,
    },
  } as AgentSurfaceMessage];
}

/** Turn the answers of a register_source proposal back into the form's values. */
export function sourceFormPrefillFromAnswers(answers: Record<string, unknown>): SourceFormPrefill {
  const pick = (value: unknown) => (Array.isArray(value) && value.length === 1 ? value[0] : value);
  const list = (value: unknown) => (Array.isArray(value) ? value.join(", ") : undefined);
  const prefill: Record<string, unknown> = {};
  for (const key of ["name", "host", "dbname", "user", "secretRef", "bucket", "prefix", "region", "endpoint", "publicBaseUrl", "presignTtl"]) {
    if (typeof answers[key] === "string") prefill[key] = answers[key];
  }
  for (const key of ["kind", "type", "backend"]) {
    const value = pick(answers[key]);
    if (typeof value === "string") prefill[key] = value;
  }
  if (typeof answers.port === "number") prefill.port = answers.port;
  const blocked = list(answers.blockedTables);
  const admin = list(answers.adminTables);
  if (blocked) prefill.blockedTables = blocked;
  if (admin) prefill.adminTables = admin;
  return sourceFormPrefillSchema.parse(prefill);
}

export const PACK_ID_SCHEMA = z.string().trim().regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, "a pack ID such as magento").max(64);

/** Show a pack's own install and configuration card; the card calls the pack APIs directly. */
export function buildPackSetupSurface(packId: string, surfaceId: string): AgentSurfaceMessage[] {
  return [{
    version: A2UI_VERSION,
    createSurface: {
      surfaceId,
      catalogId: A2UI_CATALOG_ID,
      dataModel: {},
      components: [
        { id: "root", component: "Answer", eyebrow: "PACK SETUP", title: "Connect with a pack", subtitle: "The pack holds a ready config. Enter the address and the login.", children: ["setup"] },
        { id: "setup", component: "PackSetup", packId: PACK_ID_SCHEMA.parse(packId) },
      ],
    },
  } as AgentSurfaceMessage];
}
