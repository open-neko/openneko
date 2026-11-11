import type { Operation } from "./spec";

export type Role = "admin" | "member" | "anonymous" | "revoked";

export interface Result {
  status: number;
  contentType: string;
  headers: Headers;
  body: unknown;
}

export interface Request {
  params?: Record<string, string>;
  query?: Record<string, string>;
  body?: unknown;
  form?: FormData;
  raw?: { body: string | Uint8Array<ArrayBuffer>; type: string };
  headers?: Record<string, string>;
}

export interface Target {
  baseUrl: string;
  tokens: Record<"admin" | "member", string>;
}

export function targetFromEnv(env: NodeJS.ProcessEnv = process.env): Target | null {
  const baseUrl = env.OPENNEKO_CONTRACT_URL?.trim();
  if (!baseUrl) return null;
  const admin = env.OPENNEKO_CONTRACT_ADMIN_TOKEN?.trim();
  const member = env.OPENNEKO_CONTRACT_MEMBER_TOKEN?.trim();
  if (!admin || !member) {
    throw new Error("OPENNEKO_CONTRACT_URL needs OPENNEKO_CONTRACT_ADMIN_TOKEN and OPENNEKO_CONTRACT_MEMBER_TOKEN");
  }
  return { baseUrl: baseUrl.replace(/\/+$/, ""), tokens: { admin, member } };
}

export const REVOKED_TOKEN = `onk_dev_000000000000_${"x".repeat(43)}`;

export function urlFor(target: Target, serverPath: string, op: Operation, request: Request): string {
  const path = op.path.replace(/\{(\w+)\}/g, (_, name: string) => {
    const value = request.params?.[name];
    if (value === undefined) throw new Error(`${op.key}: no value for path parameter ${name}`);
    return name === "path" ? value.split("/").map(encodeURIComponent).join("/") : encodeURIComponent(value);
  });
  const query = request.query ? `?${new URLSearchParams(request.query)}` : "";
  return `${target.baseUrl}${serverPath}${path}${query}`;
}

export async function send(target: Target, serverPath: string, op: Operation, role: Role, request: Request): Promise<Result> {
  const headers = new Headers(request.headers);
  if (role === "admin" || role === "member") headers.set("authorization", `Bearer ${target.tokens[role]}`);
  if (role === "revoked") headers.set("authorization", `Bearer ${REVOKED_TOKEN}`);
  let body: BodyInit | undefined;
  if (request.form) body = request.form;
  else if (request.raw) {
    body = typeof request.raw.body === "string" ? request.raw.body : new Blob([request.raw.body]);
    headers.set("content-type", request.raw.type);
  } else if (request.body !== undefined) {
    body = JSON.stringify(request.body);
    headers.set("content-type", "application/json");
  }
  const response = await fetch(urlFor(target, serverPath, op, request), {
    method: op.method,
    headers,
    body,
    redirect: "manual",
  });
  const contentType = response.headers.get("content-type") ?? "";
  let parsed: unknown;
  if (contentType.includes("json")) {
    const text = await response.text();
    try {
      parsed = text ? JSON.parse(text) : undefined;
    } catch {
      parsed = text;
    }
  } else if (contentType.startsWith("text/event-stream")) {
    await response.body?.cancel();
    parsed = undefined;
  } else {
    parsed = await response.text();
  }
  return { status: response.status, contentType, headers: response.headers, body: parsed };
}
