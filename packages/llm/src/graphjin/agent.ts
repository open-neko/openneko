import { packUserConnectionHeaders } from "./pack-user-connections";
export type GraphjinAgentTurn = {
  role: "user" | "assistant";
  content: string;
  status?: string;
  catalog_ids?: string[];
};

export type GraphjinAgentRequest = {
  instruction: string;
  context?: Record<string, unknown>;
  namespace?: string;
  max_steps?: number;
  return_trace?: boolean;
  history?: GraphjinAgentTurn[];
};

export type GraphjinAgentRefusal = {
  code: string;
  blocked_action?: string;
  because?: string[];
  unblock?: Array<{
    tool?: string;
    args?: Record<string, unknown>;
    reason?: string;
  }>;
  lawful_alternative?: string;
  policy_final?: boolean;
  retryable?: boolean;
};

export type GraphjinAgentResponse = {
  status: string;
  answer?: string;
  skill?: string;
  data?: unknown;
  evidence?: unknown;
  actions?: unknown;
  next?: unknown;
  refusal?: GraphjinAgentRefusal;
  notices?: Array<{ kind: string; message: string; count?: number; since?: string }>;
  errors?: Array<{ message: string; extensions?: Record<string, unknown> }>;
  usage?: unknown;
  trace?: unknown;
  trace_id?: string;
};

export type GraphjinAgentStatus = {
  status: string;
  enabled: boolean;
  ready: boolean;
  endpoint: string;
  provider: string;
  model?: string;
  api_key_configured: boolean;
  max_steps: number;
  timeout_seconds: number;
  read_only: boolean;
  return_trace: boolean;
  message: string;
};

/** Resolve GraphQL, MCP, or server-root URLs to the matching agent route. */
export function graphjinAgentEndpoint(baseUrl: string, status = false): string {
  const url = new URL(baseUrl);
  const suffix = status ? "agent/status" : "agent";
  const apiMarker = "/api/v1/";
  const markerIndex = url.pathname.indexOf(apiMarker);
  if (markerIndex >= 0) {
    const prefix = url.pathname.slice(0, markerIndex + apiMarker.length);
    const remainder = url.pathname.slice(markerIndex + apiMarker.length);
    const namespace = remainder.match(/^ns\/[^/]+\//)?.[0] ?? "";
    url.pathname = `${prefix}${namespace}${suffix}`;
  } else {
    url.pathname = `${url.pathname.replace(/\/$/, "")}/api/v1/${suffix}`;
  }
  url.search = "";
  url.hash = "";
  return url.toString();
}

async function readAgentResponse<T>(res: Response, operation: string): Promise<T> {
  const text = await res.text();
  let parsed: unknown;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = null;
  }
  if (!res.ok) {
    const detail =
      parsed && typeof parsed === "object"
        ? JSON.stringify(parsed).slice(0, 1_000)
        : text.slice(0, 1_000);
    throw new Error(`GraphJin ${operation} failed: ${res.status} ${detail}`.trim());
  }
  if (!parsed || typeof parsed !== "object") {
    throw new Error(`GraphJin ${operation} returned an invalid JSON response`);
  }
  return parsed as T;
}

export async function askGraphjinAgent(input: {
  baseUrl: string;
  token: string;
  request: GraphjinAgentRequest;
  signal?: AbortSignal;
}): Promise<GraphjinAgentResponse> {
  const res = await fetch(graphjinAgentEndpoint(input.baseUrl), {
    method: "POST",
    headers: await packUserConnectionHeaders(input.baseUrl, {
      accept: "application/json",
      "content-type": "application/json",
      authorization: `Bearer ${input.token}`,
    }),
    redirect: "error",
    cache: "no-store",
    body: JSON.stringify(input.request),
    signal: input.signal,
  });
  return readAgentResponse<GraphjinAgentResponse>(res, "agent request");
}

export async function getGraphjinAgentStatus(input: {
  baseUrl: string;
  token: string;
  signal?: AbortSignal;
}): Promise<GraphjinAgentStatus> {
  const res = await fetch(graphjinAgentEndpoint(input.baseUrl, true), {
    headers: {
      accept: "application/json",
      authorization: `Bearer ${input.token}`,
    },
    signal: input.signal,
  });
  return readAgentResponse<GraphjinAgentStatus>(res, "agent status request");
}
