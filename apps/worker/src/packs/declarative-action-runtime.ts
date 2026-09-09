import { pool } from "@neko/db";
import {
  graphjinQuery,
  mintGraphjinToken,
  resolvePackSource,
  type PackSourceSelection,
} from "@neko/llm/graphjin";
import type { ActionAdapter, ActionAdapterResolver, ActionRequestRecord } from "@neko/llm/workflows";

type ApiOperation = {
  operationId: string;
  mutationRoot: string;
  reversible?: boolean;
};

type InstalledAction = {
  definition: {
    adapter?: {
      kind?: string;
      source?: string;
      spec?: string;
      operations?: Record<string, ApiOperation>;
    };
  };
  enabled: boolean;
  readiness: string;
  readinessReason: string | null;
  installStatus: string;
  source: PackSourceSelection | null;
};

function object(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function assertSafePayload(value: unknown): void {
  const serialized = JSON.stringify(value);
  if (serialized.length > 1_000_000) throw new Error("pack action payload is too large");
  const inspect = (current: unknown): void => {
    if (!current || typeof current !== "object") return;
    if (Array.isArray(current)) {
      current.forEach(inspect);
      return;
    }
    for (const [key, child] of Object.entries(current)) {
      if (["__proto__", "prototype", "constructor"].includes(key)) {
        throw new Error("pack action payload contains an unsafe key");
      }
      inspect(child);
    }
  };
  inspect(value);
}

async function installedAction(request: ActionRequestRecord): Promise<InstalledAction> {
  const result = await pool().query<InstalledAction>(
    `select d.definition,
            d.enabled,
            d.readiness,
            d.readiness_reason as "readinessReason",
            i.status as "installStatus",
            i.config->'_runtime'->'source' as source
       from pack_action_definition d
       join pack_artifact a
         on a.org_id=d.org_id
        and a.artifact_kind='action'
        and a.target_ref=d.kind
       join pack_install i
         on i.id=a.pack_install_id
        and i.org_id=a.org_id
      where d.org_id=$1 and d.kind=$2
      order by i.installed_at desc nulls last
      limit 1`,
    [request.orgId, request.kind],
  );
  const action = result.rows[0];
  if (!action) throw new Error(`no installed pack owns action ${request.kind}`);
  if (!action.enabled || action.readiness !== "ready" || action.installStatus !== "installed") {
    throw new Error(`pack action ${request.kind} is unavailable: ${action.readinessReason ?? action.installStatus}`);
  }
  if (!action.source) throw new Error(`pack action ${request.kind} has no reviewed data source binding`);
  return action;
}

function responseJson(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

export const declarativePackActionAdapter: ActionAdapter = async ({ request }) => {
  assertSafePayload(request.payload);
  const action = await installedAction(request);
  const adapter = action.definition.adapter;
  if (adapter?.kind !== "graphjin_api_operation" || !adapter.operations) {
    throw new Error(`no declarative API adapter exists for action ${request.kind}`);
  }
  const operationName = request.payload.operation;
  if (typeof operationName !== "string" || !Object.hasOwn(adapter.operations, operationName)) {
    throw new Error(`pack action ${request.kind} requested an unsupported operation`);
  }
  const operation = adapter.operations[operationName]!;
  if (!/^[_A-Za-z][_0-9A-Za-z]*$/.test(operation.mutationRoot)) {
    throw new Error(`pack action ${request.kind} has an invalid mutation root`);
  }
  const path = request.payload.path === undefined ? {} : object(request.payload.path, "pack action path");
  const query = request.payload.query === undefined ? {} : object(request.payload.query, "pack action query");
  const selectedSource = action.source;
  if (!selectedSource) throw new Error(`pack action ${request.kind} has no reviewed data source binding`);
  const source = await resolvePackSource(request.orgId, selectedSource);
  const result = await graphjinQuery<Record<string, {
    ok?: boolean;
    status_code?: number;
    operation_id?: string;
    request_id?: string;
    response_json?: unknown;
  }>>({
    baseUrl: source.graphqlUrl,
    headers: {
      authorization: `Bearer ${mintGraphjinToken({
        orgId: request.orgId,
        userId: request.approvedByUserId ?? request.actorUserId ?? "pack-executor",
        role: "pack_api_executor",
        ttlSeconds: 60,
      })}`,
    },
    role: "pack_api_executor",
    query: `mutation ExecutePackAction($call: JSON!) { ${operation.mutationRoot}(call: $call) { ok status_code operation_id request_id response_json } }`,
    variables: {
      call: {
        ...(Object.keys(path).length > 0 ? { path } : {}),
        ...(Object.keys(query).length > 0 ? { query } : {}),
        ...(request.payload.body !== undefined ? { body: request.payload.body } : {}),
      },
    },
    signal: AbortSignal.timeout(30_000),
  });
  const response = result.data?.[operation.mutationRoot];
  if (result.errors?.length || !response) {
    throw new Error(result.errors?.map((error) => error.message).join("; ") || "pack API mutation returned no result");
  }
  if (!response.ok || Number(response.status_code ?? 500) >= 400) {
    throw new Error(`pack API mutation failed with HTTP ${response.status_code ?? "unknown"}`);
  }
  return {
    externalRef: response.request_id ?? null,
    commandOrOperation: response.operation_id ?? operation.operationId,
    result: {
      operation: operationName,
      statusCode: Number(response.status_code ?? 200),
      response: responseJson(response.response_json),
    },
  };
};

export const resolveDeclarativePackActionAdapter: ActionAdapterResolver = async (request) => {
  const result = await pool().query<{ owned: boolean }>(
    "select true as owned from pack_action_definition where org_id=$1 and kind=$2 limit 1",
    [request.orgId, request.kind],
  );
  return result.rows[0]?.owned ? declarativePackActionAdapter : null;
};
