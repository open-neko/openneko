import type { Request, Result, Role } from "./client";
import { FIXTURE } from "./fixtures";

export interface Context {
  ids: Record<string, string>;
  origin: string;
}

export interface Case {
  op: string;
  name: string;
  as?: Role;
  status: number;
  request?: Request | ((c: Context) => Request);
  check?: (result: Result, c: Context) => void;
}

export function newContext(baseUrl: string): Context {
  return { ids: { ...FIXTURE }, origin: new URL(baseUrl).origin };
}

export function body(result: Result): Record<string, unknown> {
  return result.body as Record<string, unknown>;
}
