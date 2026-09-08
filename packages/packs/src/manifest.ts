import { z } from "zod";
import { packConnectorSchema } from "./connector.js";

const slug = z
  .string()
  .regex(/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/, "must be a lowercase slug");
const version = z
  .string()
  .regex(/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/, "must be a semantic version");
const relativePath = z.string().min(1).superRefine((value, ctx) => {
  if (value.includes("\\")) {
    ctx.addIssue({ code: "custom", message: "must use forward slashes" });
  }
  if (value.startsWith("/") || value.split("/").some((part) => part === "..")) {
    ctx.addIssue({ code: "custom", message: "must stay within the pack root" });
  }
});

const metadataSchema = z
  .object({
    id: slug,
    name: z.string().min(1),
    version,
    publisher: slug,
    category: slug,
  })
  .strict();

const applicationCompatibilitySchema = z
  .object({
    id: slug,
    editions: z.array(slug).min(1),
    versions: z.string().min(1),
  })
  .strict();

const databaseCompatibilitySchema = z
  .object({
    engine: z.enum(["mariadb", "mysql", "postgres"]),
    versions: z.string().min(1),
  })
  .strict();

const compatibilitySchema = z
  .object({
    openneko: z.string().min(1),
    graphjin: z
      .object({
        analytics: z.string().min(1),
        operator: z.string().min(1),
      })
      .strict().optional(),
    applications: z.array(applicationCompatibilitySchema),
    databases: z.array(databaseCompatibilitySchema),
  })
  .strict();

const inputSchema = z
  .object({
    key: z.string().regex(/^[a-z][a-z0-9_.-]+$/),
    type: z.enum(["string", "url", "integer", "enum", "timezone", "boolean"]),
    required: z.boolean().optional(),
    default: z.union([z.string(), z.number(), z.boolean()]).optional(),
    discover: z.boolean().optional(),
    values: z.array(z.union([z.string(), z.number(), z.boolean()])).optional(),
    description: z.string().optional(),
  })
  .strict()
  .superRefine((input, ctx) => {
    if (input.type === "enum" && (!input.values || input.values.length === 0)) {
      ctx.addIssue({ code: "custom", message: "enum inputs require values", path: ["values"] });
    }
    if (input.type !== "enum" && input.values) {
      ctx.addIssue({ code: "custom", message: "values are valid only for enum inputs", path: ["values"] });
    }
  });

const secretSchema = z
  .object({
    key: z.string().regex(/^[a-z][a-z0-9_.-]+$/),
    purpose: z.enum(["graphjin_source", "graphjin_api_auth", "pack_runtime"]),
    required: z.boolean().optional().default(true),
  })
  .strict();

const graphjinArtifactsSchema = z
  .object({
    sources: relativePath,
    relationships: relativePath,
    specs: z.array(relativePath),
    savedQueries: relativePath,
  })
  .strict();

const artifactsSchema = z
  .object({
    graphjin: graphjinArtifactsSchema.optional(),
    metrics: relativePath.optional(),
    workflows: relativePath.optional(),
    watchers: relativePath.optional(),
    actions: relativePath.optional(),
    policies: relativePath.optional(),
    skills: z.array(relativePath),
  })
  .strict();

const healthSchema = z
  .object({
    requiredPreflight: z.array(slug),
    readiness: z.record(slug, z.array(slug)),
    postInstall: z.array(slug),
    postWriteCanary: z.array(slug),
  })
  .strict();

export const solutionPackManifestSchema = z
  .object({
    apiVersion: z.literal("openneko.app/v1"),
    kind: z.literal("SolutionPack"),
    metadata: metadataSchema,
    compatibility: compatibilitySchema,
    inputs: z.array(inputSchema),
    secrets: z.array(secretSchema),
    artifacts: artifactsSchema,
    health: healthSchema,
    connectors: z.array(packConnectorSchema).optional(),
  })
  .strict()
  .superRefine((manifest, ctx) => {
    if (manifest.artifacts.graphjin && !manifest.compatibility.graphjin) {
      ctx.addIssue({ code: "custom", message: "GraphJin artifacts require GraphJin compatibility", path: ["compatibility", "graphjin"] });
    }
    const connectorIds = (manifest.connectors ?? []).map(connector => connector.id);
    if (new Set(connectorIds).size !== connectorIds.length) ctx.addIssue({ code: "custom", message: "duplicate connector id", path: ["connectors"] });
    for (const field of ["inputs", "secrets"] as const) {
      const seen = new Set<string>();
      manifest[field].forEach((entry, index) => {
        if (seen.has(entry.key)) {
          ctx.addIssue({ code: "custom", message: `duplicate ${field} key`, path: [field, index, "key"] });
        }
        seen.add(entry.key);
      });
    }
  });

export type SolutionPackManifest = z.infer<typeof solutionPackManifestSchema>;

export function parseManifest(input: unknown): SolutionPackManifest {
  return solutionPackManifestSchema.parse(input);
}
