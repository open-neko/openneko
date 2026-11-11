import { readFileSync } from "node:fs";
import { join } from "node:path";
import { parse } from "yaml";
import type { Schema } from "./validate";

export const METHODS = ["get", "post", "put", "patch", "delete"] as const;
export type Access = "public" | "token" | "signed-in" | "admin";

export interface Response {
  description?: string;
  content?: Record<string, { schema?: Schema }>;
}

export interface Operation {
  key: string;
  method: string;
  path: string;
  operationId: string;
  access: Access;
  responses: Record<string, Response>;
}

export interface Contract {
  servers: { url: string }[];
  paths: Record<string, Record<string, { operationId: string; "x-access": Access; responses?: Record<string, Response> }>>;
  components?: { schemas?: Record<string, Schema> };
}

export const contractFile = join(import.meta.dirname, "..", "..", "..", "api", "openapi.yaml");

export function loadContract(file = contractFile): Contract {
  return parse(readFileSync(file, "utf8")) as Contract;
}

export function operations(contract: Contract): Operation[] {
  return Object.entries(contract.paths).flatMap(([path, item]) =>
    METHODS.filter((m) => item[m]).map((method) => {
      const op = item[method]!;
      return {
        key: `${method.toUpperCase()} ${path}`,
        method: method.toUpperCase(),
        path,
        operationId: op.operationId,
        access: op["x-access"],
        responses: op.responses ?? {},
      };
    }),
  );
}
