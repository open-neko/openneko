import type { CallToolResult, Tool } from "@modelcontextprotocol/sdk/types.js";
import {
  callGraphjinMcpTool,
  listGraphjinMcpTools,
} from "../graphjin/mcp-client";
import { assertGraphjinMutationAllowed } from "../graphjin/sources-config";
import type { DataBackend, DataCaller, DataQuery, DataQueryResult, DataToolCall } from "./index";

type Principal = { userId: string | null; role: "admin" | "member" | "service" };

export function graphjinReadPrincipal(actor: {
  userId: string | null;
  role: string | null;
}): Principal {
  if (actor.role === "admin" || actor.role === "member") {
    return { userId: actor.userId, role: actor.role };
  }
  if (actor.role === "service") {
    return { userId: null, role: "service" };
  }
  throw new Error("GraphJin read actor has an invalid role");
}

export function graphjinDevelopmentAuthHeaders(
  principal: Principal,
  nodeEnv = process.env.NODE_ENV,
): Record<string, string> {
  if (nodeEnv === "production") {
    throw new Error("GraphJin development header auth is forbidden in production");
  }
  return {
    "X-User-ID": principal.userId ?? "openneko-service",
    "X-User-Role": principal.role,
  };
}

export function isInternalGraphjinURL(url: string): boolean {
  try {
    const candidate = new URL(url).host;
    const internal = new URL(process.env.OPENNEKO_GRAPHJIN_URL ?? "http://127.0.0.1:8089").host;
    return candidate === internal;
  } catch {
    return true;
  }
}

async function callerPrincipal(caller: DataCaller): Promise<Principal> {
  if (!caller.runId) return { userId: null, role: "service" };
  const { getWorkRunActor } = await import("../work/personas");
  return graphjinReadPrincipal(await getWorkRunActor(caller.runId, caller.orgId));
}

async function callerToken(caller: DataCaller): Promise<string> {
  const { mintGraphjinToken } = await import("../graphjin/token");
  const principal = await callerPrincipal(caller);
  let claims: { roles: string[]; groups: string[] } | null = null;
  if (principal.userId && principal.role === "member") {
    const { getGroupGrantsEnabled, graphjinGroupClaims } = await import("@neko/db");
    if (await getGroupGrantsEnabled(caller.orgId)) {
      claims = await graphjinGroupClaims(caller.orgId, principal.userId);
    }
  }
  return mintGraphjinToken({
    orgId: caller.orgId,
    userId: principal.userId,
    role: principal.role,
    ...(claims ? { groupRoles: claims.roles, groups: claims.groups } : {}),
  });
}

async function mcpAccess(caller: DataCaller): Promise<{ mcpUrl: string; headers: Record<string, string> }> {
  const { and, data_source, db, desc, eq } = await import("@neko/db");
  const [source] = await db()
    .select({
      mcpUrl: data_source.mcp_url,
      graphqlUrl: data_source.graphql_url,
      kind: data_source.kind,
      authMode: data_source.auth_mode,
    })
    .from(data_source)
    .where(and(eq(data_source.org_id, caller.orgId), eq(data_source.enabled, true)))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  if (!source?.mcpUrl) {
    throw new Error("no enabled GraphJin MCP endpoint configured");
  }
  if (source.kind !== "graphjin") {
    throw new Error("the default enabled data source is not a GraphJin server");
  }
  if (isInternalGraphjinURL(source.graphqlUrl) || isInternalGraphjinURL(source.mcpUrl)) {
    throw new Error("refusing to expose the OpenNeko internal GraphJin MCP");
  }
  const headers: Record<string, string> = {};
  if (source.authMode === "jwt") {
    headers.authorization = `Bearer ${await callerToken(caller)}`;
  } else if (source.authMode === "development") {
    Object.assign(headers, graphjinDevelopmentAuthHeaders(await callerPrincipal(caller)));
  }
  return { mcpUrl: source.mcpUrl, headers };
}

export const graphjinDataBackend: DataBackend = {
  name: "graphjin",

  async query(input: DataQuery): Promise<DataQueryResult> {
    const { data_source, db, desc, eq } = await import("@neko/db");
    const [source] = await db()
      .select({ graphqlUrl: data_source.graphql_url, authMode: data_source.auth_mode })
      .from(data_source)
      .where(eq(data_source.org_id, input.orgId))
      .orderBy(desc(data_source.is_default), data_source.created_at)
      .limit(1);
    if (!source?.graphqlUrl) {
      throw new Error("no GraphJin GraphQL endpoint configured");
    }
    const headers: Record<string, string> = {};
    if (source.authMode === "jwt") {
      headers.authorization = `Bearer ${await callerToken(input)}`;
    }
    const { graphjinQuery } = await import("../graphjin/client");
    return graphjinQuery({
      baseUrl: source.graphqlUrl,
      query: input.query,
      variables: input.variables,
      operationName: input.operationName,
      headers,
      signal: AbortSignal.timeout(60_000),
    });
  },

  async listTools(caller: DataCaller): Promise<Tool[]> {
    const access = await mcpAccess(caller);
    return listGraphjinMcpTools({
      baseUrl: access.mcpUrl,
      headers: access.headers,
      signal: AbortSignal.timeout(60_000),
    });
  },

  async callTool(input: DataToolCall): Promise<CallToolResult> {
    if (input.name === "execute_graphql") {
      await assertGraphjinMutationAllowed(
        String(input.arguments?.query ?? ""),
        process.env.OPENNEKO_GRAPHJIN_CONFIG,
      );
    }
    const access = await mcpAccess(input);
    return callGraphjinMcpTool(
      {
        baseUrl: access.mcpUrl,
        headers: access.headers,
        // Governed operations can outlive a normal query.
        signal: AbortSignal.timeout(180_000),
      },
      { name: input.name, arguments: input.arguments },
    );
  },
};
