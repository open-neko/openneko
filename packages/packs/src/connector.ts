import { z } from "zod";

const identifier = z.string().regex(/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/);
const executable = z.string().regex(/^\/(?:app|usr\/bin|usr\/local\/bin)\/[a-zA-Z0-9_./-]+$/)
  .refine(value => !value.split("/").includes(".."), "executable must not contain parent paths");

/** Pack-owned execution contract. No plugin registration or plugin types. */
export const packConnectorSchema = z.object({
  id: identifier,
  image: z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$/, "connector image must have a SHA-256 digest"),
  entrypoint: executable,
  auth: z.object({
    label: z.string().min(1).max(100),
    // The pack owns provider endpoints and validates issuer/token responses.
    authorizationOrigin: z.url().refine(value => new URL(value).protocol === "https:" && new URL(value).origin === value, "use an HTTPS origin"),
    scopes: z.array(z.string().min(1).max(500)).min(1),
    credentialVersion: z.string().min(1).max(100),
  }).strict().optional(),
  operations: z.array(z.object({
    id: identifier,
    description: z.string().min(1).max(1000),
    effect: z.enum(["read", "write"]),
  }).strict()).min(1),
  network: z.array(z.object({
    host: z.string().regex(/^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$/),
    port: z.number().int().min(1).max(65535).default(443),
    binary: executable,
  }).strict()).default([]),
}).strict().superRefine((connector, ctx) => {
  const ids = connector.operations.map(operation => operation.id);
  if (new Set(ids).size !== ids.length) ctx.addIssue({ code: "custom", message: "duplicate connector operation" });
});

export type PackConnector = z.infer<typeof packConnectorSchema>;

export const packCredentialSchema = z.object({
  accountId: z.string().min(1).max(500),
  label: z.string().min(1).max(200),
  scopes: z.array(z.string().min(1)),
  expiresAt: z.number().finite().positive(),
  tokens: z.record(z.string(), z.unknown()),
}).strict();

export const packActionPayloadSchema = z.object({
  input: z.record(z.string(), z.unknown()),
  accountId: z.uuid().optional(),
  attachments: z.array(z.object({
    name: z.string().min(1).max(200).regex(/^[^/\\]+$/),
    mediaType: z.string().min(1).max(200),
    contentBase64: z.string(),
    sha256: z.string().regex(/^[a-f0-9]{64}$/),
  }).strict()).max(20).optional(),
  _pack: z.object({ binding: z.string(), seal: z.string() }).strict().optional(),
}).strict();

export const packOperationResultSchema = z.object({
  status: z.enum(["succeeded", "failed", "reconcile_required"]),
  receipt: z.record(z.string(), z.unknown()),
  output: z.record(z.string(), z.unknown()).optional(),
}).strict();
