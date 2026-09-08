import { z } from "zod";

const identifier = z.string().regex(/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/);
const executable = z.string().regex(/^\/(?:app|usr\/bin|usr\/local\/bin)\/[a-zA-Z0-9_./-]+$/)
  .refine(value => !value.split("/").includes(".."), "executable must not contain parent paths");

/** Pack-owned execution contract. No plugin registration or plugin types. */
export const packConnectorSchema = z.object({
  id: identifier,
  image: z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$/, "connector image must have a SHA-256 digest"),
  entrypoint: executable,
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
