import { createHmac } from "node:crypto";
import { deriveSigningSecret } from "@neko/secret-crypt";

export const SERVE_TOKEN_LABEL = "openneko-serve:v1";
const TOKEN_TTL_SECONDS = 60;

export type FilesIdentity = { orgId: string; userId: string };

export type FilePassage = { location: string; snippet: string };
export type FileTableColumn = { name: string; type: string };
export type FileTable = {
  name: string;
  sheet: string;
  raw?: boolean;
  header_row?: number;
  rows: number;
  columns: FileTableColumn[];
};
export type FileSearchMatch = {
  path: string;
  score: number;
  passages: FilePassage[];
  tables?: FileTable[];
};
export type FileSearchResult = { matches: FileSearchMatch[] };
export type FileReadResult = {
  path: string;
  text: string;
  offset: number;
  total_chars: number;
  truncated: boolean;
  tables: FileTable[];
};
export type FileSqlResult = {
  columns: string[];
  rows: unknown[][];
  truncated?: boolean;
};
export type FileInfo = { path: string; size: number; modified_at: string };

export class FilesServiceError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "FilesServiceError";
  }
}

export function serveUrl(): string | null {
  const raw = process.env.OPENNEKO_SERVE_URL?.trim();
  return raw ? raw.replace(/\/+$/, "") : null;
}

export function filesEnabled(): boolean {
  return serveUrl() !== null;
}

export function mintServeToken(
  identity: FilesIdentity,
  options: { key?: Buffer; nowMs?: number; ttlSeconds?: number } = {},
): string {
  const key = options.key ?? deriveSigningSecret(SERVE_TOKEN_LABEL);
  const now = Math.floor((options.nowMs ?? Date.now()) / 1000);
  const enc = (v: string) => Buffer.from(v).toString("base64url");
  const head = enc(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const body = enc(
    JSON.stringify({
      org: identity.orgId,
      sub: identity.userId,
      exp: now + (options.ttlSeconds ?? TOKEN_TTL_SECONDS),
    }),
  );
  const sig = createHmac("sha256", key).update(`${head}.${body}`).digest("base64url");
  return `${head}.${body}.${sig}`;
}

async function request<T>(
  identity: FilesIdentity,
  method: string,
  path: string,
  body?: BodyInit,
  headers: Record<string, string> = {},
): Promise<T> {
  const base = serveUrl();
  if (!base) {
    throw new FilesServiceError("File search is off: OPENNEKO_SERVE_URL is not set.", 503);
  }
  const res = await fetch(`${base}${path}`, {
    method,
    headers: { authorization: `Bearer ${mintServeToken(identity)}`, ...headers },
    ...(body !== undefined ? { body } : {}),
    signal: AbortSignal.timeout(60_000),
  });
  const text = await res.text();
  let parsed: unknown = null;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  if (!res.ok) {
    const message =
      parsed && typeof parsed === "object" && typeof (parsed as { error?: unknown }).error === "string"
        ? (parsed as { error: string }).error
        : `openneko serve answered ${res.status}`;
    throw new FilesServiceError(message, res.status);
  }
  return parsed as T;
}

const json = (v: unknown) => JSON.stringify(v);
const JSON_HEADERS = { "content-type": "application/json" };

export function searchFiles(identity: FilesIdentity, query: string, limit?: number) {
  return request<FileSearchResult>(identity, "POST", "/v1/files/search", json({ query, ...(limit ? { limit } : {}) }), JSON_HEADERS);
}

export function readFileText(
  identity: FilesIdentity,
  path: string,
  options: { offset?: number; maxChars?: number } = {},
) {
  return request<FileReadResult>(
    identity,
    "POST",
    "/v1/files/read",
    json({
      path,
      ...(options.offset ? { offset: options.offset } : {}),
      ...(options.maxChars ? { max_chars: options.maxChars } : {}),
    }),
    JSON_HEADERS,
  );
}

export function queryFileTables(identity: FilesIdentity, sql: string) {
  return request<FileSqlResult>(identity, "POST", "/v1/files/sql", json({ sql }), JSON_HEADERS);
}

export function putFile(
  identity: FilesIdentity,
  path: string,
  data: Blob | Uint8Array<ArrayBuffer>,
  contentType?: string,
) {
  return request<FileInfo>(
    identity,
    "PUT",
    `/v1/files/content?path=${encodeURIComponent(path)}`,
    data,
    contentType ? { "content-type": contentType } : {},
  );
}

export function deleteFile(identity: FilesIdentity, path: string) {
  return request<{ deleted: boolean }>(identity, "DELETE", `/v1/files/content?path=${encodeURIComponent(path)}`);
}

export function listFiles(identity: FilesIdentity, prefix?: string) {
  const q = prefix ? `?prefix=${encodeURIComponent(prefix)}` : "";
  return request<{ files: FileInfo[] }>(identity, "GET", `/v1/files${q}`);
}
