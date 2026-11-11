import type { CallToolResult, Tool } from "@modelcontextprotocol/sdk/types.js";
import { holds } from "@neko/db";
import { entitlementActorForRun } from "../work/entitlement-scope";
import { graphjinDataBackend } from "./graphjin";

export interface DataCaller {
  orgId: string;
  runId?: string | null;
}

export interface DataQuery extends DataCaller {
  query: string;
  variables?: Record<string, unknown>;
  operationName?: string;
}

export interface DataQueryResult {
  data: unknown;
  errors?: Array<{ message: string; path?: (string | number)[] }>;
}

export interface DataToolCall extends DataCaller {
  name: string;
  arguments?: Record<string, unknown>;
}

export interface DataBackend {
  readonly name: string;
  query(input: DataQuery): Promise<DataQueryResult>;
  listTools(caller: DataCaller): Promise<Tool[]>;
  callTool(input: DataToolCall): Promise<CallToolResult>;
}

let backend: DataBackend = graphjinDataBackend;

export function dataBackend(): DataBackend {
  return backend;
}

export function setDataBackend(next: DataBackend | undefined): void {
  backend = next ?? graphjinDataBackend;
}

export function assertReadOnlyGraphql(query: string): void {
  const text = query.trim();
  if (!text) throw new Error("GraphJin read query is empty");
  // Rejecting the words even in a string or comment is a false positive at
  // worst; accepting either operation makes the read path a write path.
  if (/\b(?:mutation|subscription)\b/i.test(text)) {
    throw new Error("GraphJin agent broker allows query operations only");
  }
  if (!text.startsWith("{") && !/^query\b/i.test(text)) {
    throw new Error("GraphJin agent broker requires an explicit query operation");
  }
}

async function assertRunHoldsDataSource(caller: DataCaller): Promise<void> {
  if (!caller.runId) return;
  const actor = await entitlementActorForRun(caller.orgId, caller.runId);
  const { heldItems } = await import("@neko/db");
  const held = actor ? await heldItems(actor, "data_source") : new Set<string>();
  if (held !== "*" && held.size === 0) throw new Error("No data source is available to this run.");
}

async function savedQueryRefusal(input: DataToolCall): Promise<CallToolResult | null> {
  if (input.name !== "execute_saved_query" || !input.runId) return null;
  const actor = await entitlementActorForRun(input.orgId, input.runId);
  const name = String(input.arguments?.name ?? "");
  if (actor && (await holds(actor, "saved_query", name)).allowed) return null;
  return {
    isError: true,
    content: [{ type: "text", text: `Saved query "${name}" is not available to this run.` }],
  };
}

export const dataAccess = {
  async query(input: DataQuery): Promise<DataQueryResult> {
    assertReadOnlyGraphql(input.query);
    await assertRunHoldsDataSource(input);
    return backend.query(input);
  },

  async listTools(caller: DataCaller): Promise<Tool[]> {
    await assertRunHoldsDataSource(caller);
    return backend.listTools(caller);
  },

  async callTool(input: DataToolCall): Promise<CallToolResult> {
    const refusal = await savedQueryRefusal(input);
    if (refusal) return refusal;
    await assertRunHoldsDataSource(input);
    return backend.callTool(input);
  },
};
