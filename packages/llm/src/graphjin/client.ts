import { traceContextHeaders } from "@neko/telemetry";
import { packUserConnectionHeaders, type ConnectionBinding } from "./pack-user-connections";
import { Agent } from "undici";

export type GraphjinQueryOptions = {
  connectionBindings?: ConnectionBinding[];
  /** Trusted configuration-only requests must never attach personal tokens. */
  configurationOnly?: boolean;
  /** Full GraphJin GraphQL endpoint URL, e.g. `http://host:8080/api/v1/graphql`. */
  baseUrl: string;
  query: string;
  variables?: Record<string, unknown>;
  operationName?: string;
  /** Sent as `X-Role` header for GraphJin role-based access. */
  role?: string;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  /**
   * A gj_config preview or apply can rebuild GraphJin's runtime, which
   * rediscovers every source. On a large schema that outlasts fetch's
   * 5-minute wait for response headers.
   */
  longRunning?: boolean;
};

export type GraphjinQueryResult<T = unknown> = {
  data: T | null;
  errors?: Array<{ message: string; path?: (string | number)[] }>;
};

const longRunningTimeoutMs = 45 * 60_000;
let longRunningAgent: Agent | undefined;

function longRunningDispatcher(): Agent {
  longRunningAgent ??= new Agent({ headersTimeout: longRunningTimeoutMs, bodyTimeout: longRunningTimeoutMs });
  return longRunningAgent;
}

export async function graphjinQuery<T = unknown>(
  opts: GraphjinQueryOptions,
): Promise<GraphjinQueryResult<T>> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(opts.role ? { "X-Role": opts.role } : {}),
    ...traceContextHeaders(),
    ...(opts.headers ?? {}),
  };
  const res = await fetch(opts.baseUrl, {
    method: "POST",
    headers: opts.configurationOnly ? headers : await packUserConnectionHeaders(opts.baseUrl, headers, opts.connectionBindings),
    redirect: "error",
    cache: "no-store",
    body: JSON.stringify({
      query: opts.query,
      variables: opts.variables ?? {},
      operationName: opts.operationName,
    }),
    signal: opts.signal,
    ...(opts.longRunning ? { dispatcher: longRunningDispatcher() } : {}),
  } as RequestInit);
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`graphjin query failed: ${res.status} ${text.slice(0, 500)}`);
  }
  const body = (await res.json()) as {
    data?: T | null;
    errors?: unknown[];
  };
  const errors = Array.isArray(body.errors)
    ? body.errors.map((item) => {
        if (typeof item === "string") return { message: item };
        if (item && typeof item === "object") {
          const error = item as {
            message?: unknown;
            path?: unknown;
          };
          return {
            message:
              typeof error.message === "string"
                ? error.message
                : JSON.stringify(item),
            ...(Array.isArray(error.path)
              ? { path: error.path as (string | number)[] }
              : {}),
          };
        }
        return { message: String(item) };
      })
    : undefined;
  return {
    data: body.data ?? null,
    ...(errors?.length ? { errors } : {}),
  };
}
