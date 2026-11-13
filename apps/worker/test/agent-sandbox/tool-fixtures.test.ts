import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { buildMultiplexedBridgeServer } from "../../src/agent-sandbox/mcp-bridge.js";
import type { BrokerControlPlane } from "../../src/agent-sandbox/broker-client.js";
import { parseGraphjinMcpToolPolicy } from "@neko/llm/sandbox-runtime";

// The Go tool server (apps/openneko/internal/webfront/turntools.go) must list
// the same tools and give the same results. Run with UPDATE_PROMPT_FIXTURES=1
// to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../openneko/internal/webfront/testdata/turntools.json");

type Reply = { value?: unknown; throws?: string };
type CallCase = {
  name: string;
  servers: string[];
  memoryReadOnly?: boolean;
  policy?: string;
  listFirst?: boolean;
  graphjinTools?: unknown[];
  listThrows?: string;
  tool: string;
  arguments?: Record<string, unknown>;
  reply?: Reply;
};

const MEMORY = { id: "44444444-4444-4444-8444-444444444444", text: "Revenue is net of refunds.", kind: "business_rule", scope: "global", userId: null };
const MATCHES = { matches: [{ path: "uploads/q3.xlsx", score: 2.5, passages: [{ location: "sheet Sales", snippet: "net revenue" }], tables: [{ name: "q3__sales", sheet: "Sales", rows: 12, columns: [{ name: "region", type: "VARCHAR" }] }] }] };

const GJ_TOOLS = [
  { name: "execute_graphql", description: "Run one GraphQL operation.", inputSchema: { type: "object", properties: { query: { type: "string" }, variables: { type: "object", additionalProperties: true } }, required: ["query"] }, annotations: { destructiveHint: true, readOnlyHint: false } },
  { name: "query_catalog", description: "Search the catalog.", inputSchema: { type: "object", properties: { ids: { type: "array", items: { type: "string" } }, kind: { type: "string", enum: ["table", "column"] } } } },
  { name: "graphql_help", inputSchema: { type: "object" } },
  { name: "validate_where_clause", description: "Check a where clause.", inputSchema: { type: "object", properties: { table: { type: "string" }, where: { type: "object" } }, required: ["table", "where"] } },
  { name: "execute_saved_query", description: "Run a saved query.", inputSchema: { type: "object", properties: { name: { type: "string" }, variables: { type: "object" } }, required: ["name"] } },
  { name: "ask_graphjin_agent", description: "Ask GraphJin's agent.", inputSchema: { type: "object", properties: { question: { type: "string" } } } },
  { title: "Sources", name: "list_sources", description: "", inputSchema: { type: "object", properties: {}, additionalProperties: false }, outputSchema: { type: "object", properties: { sources: { type: "array" } } }, _meta: { "x-openneko": 1 }, extraField: "kept" },
];
const GQL_ROWS = { content: [{ type: "text", text: JSON.stringify({ data: { orders: [{ id: 1, total: 12.5 }, { id: 2, total: 7 }] } }) }] };

const CALLS: CallCase[] = [
  { name: "memory search", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue", limit: 3 }, reply: { value: [{ memory: MEMORY, score: 0.91 }] } },
  { name: "memory search default limit", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue" }, reply: { value: [] } },
  { name: "memory search short query", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "r" } },
  { name: "memory search limit too high", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue", limit: 21 } },
  { name: "memory search fraction limit", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue", limit: 2.5 } },
  { name: "memory search fails", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue" }, reply: { throws: "embedding service is off" } },
  { name: "memory search two faults", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "r", limit: 25.5 } },
  { name: "memory search string limit", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue", limit: "3" } },
  { name: "memory search null limit", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "revenue", limit: null } },
  { name: "memory search long query", servers: ["neko_memory"], tool: "memory_search", arguments: { query: "x".repeat(801) } },
  { name: "memory search object query", servers: ["neko_memory"], tool: "memory_search", arguments: { query: { a: 1 }, limit: [1] } },
  { name: "memory save pinned text", servers: ["neko_memory"], tool: "memory_save", arguments: { text: "Use the EU calendar.", pinned: "yes", scope: true } },
  { name: "memory update upper id", servers: ["neko_memory"], tool: "memory_update", arguments: { memoryId: "44444444-4444-4444-8444-44444444444A", text: "Revenue is net." }, reply: { value: { id: MEMORY.id } } },
  { name: "memory update unicode text", servers: ["neko_memory"], tool: "memory_update", arguments: { memoryId: MEMORY.id, text: "€€€€" } },
  { name: "memory save defaults", servers: ["neko_memory"], tool: "memory_save", arguments: { text: "Revenue is net of refunds." }, reply: { value: { id: MEMORY.id } } },
  { name: "memory save all fields", servers: ["neko_memory"], tool: "memory_save", arguments: { text: "Use the EU calendar.", kind: "preference", scope: "thread", pinned: false }, reply: { value: { id: MEMORY.id } } },
  { name: "memory save bad kind", servers: ["neko_memory"], tool: "memory_save", arguments: { text: "Use the EU calendar.", kind: "rumour" } },
  { name: "memory save missing text", servers: ["neko_memory"], tool: "memory_save", arguments: {} },
  { name: "memory save extra field", servers: ["neko_memory"], tool: "memory_save", arguments: { text: "Use the EU calendar.", userId: "someone-else" }, reply: { value: { id: MEMORY.id } } },
  { name: "memory update", servers: ["neko_memory"], tool: "memory_update", arguments: { memoryId: MEMORY.id, text: "Revenue is net of refunds and tax." }, reply: { value: { id: MEMORY.id } } },
  { name: "memory update not visible", servers: ["neko_memory"], tool: "memory_update", arguments: { memoryId: MEMORY.id, text: "Revenue is net of refunds and tax." }, reply: { throws: "work memory not found" } },
  { name: "memory update bad id", servers: ["neko_memory"], tool: "memory_update", arguments: { memoryId: "not-a-uuid", text: "Revenue is net." } },
  { name: "memory save read-only", servers: ["neko_memory"], memoryReadOnly: true, tool: "memory_save", arguments: { text: "Revenue is net of refunds." } },
  { name: "guides find", servers: ["neko_guides"], tool: "guides_find", arguments: { query: "refund report", limit: 3 }, reply: { value: [{ name: "refunds", score: 0.8 }] } },
  { name: "guides find no limit", servers: ["neko_guides"], tool: "guides_find", arguments: { query: "refund report" }, reply: { value: [] } },
  { name: "guides find zero limit", servers: ["neko_guides"], tool: "guides_find", arguments: { query: "refund report", limit: 0 } },
  { name: "files search", servers: ["neko_files"], tool: "files_search", arguments: { query: "net revenue", limit: 4 }, reply: { value: MATCHES } },
  { name: "files search no limit", servers: ["neko_files"], tool: "files_search", arguments: { query: "net revenue" }, reply: { value: { matches: [] } } },
  { name: "files search fails", servers: ["neko_files"], tool: "files_search", arguments: { query: "net revenue" }, reply: { throws: "file search is not available: serve is not set" } },
  { name: "files search empty query", servers: ["neko_files"], tool: "files_search", arguments: { query: "" } },
  { name: "files read", servers: ["neko_files"], tool: "files_read_text", arguments: { path: "uploads/q3.xlsx", offset: 100, maxChars: 2000 }, reply: { value: { path: "uploads/q3.xlsx", text: "[sheet Sales]\nregion,total", offset: 100, total_chars: 300, truncated: false, tables: [] } } },
  { name: "files read small max", servers: ["neko_files"], tool: "files_read_text", arguments: { path: "uploads/q3.xlsx", maxChars: 500 } },
  { name: "files read negative offset", servers: ["neko_files"], tool: "files_read_text", arguments: { path: "uploads/q3.xlsx", offset: -1 } },
  { name: "files query", servers: ["neko_files"], tool: "files_query_tables", arguments: { sql: "select region from q3__sales" }, reply: { value: { columns: ["region"], rows: [["EU"], ["US"]] } } },
  { name: "files query fails", servers: ["neko_files"], tool: "files_query_tables", arguments: { sql: "drop table q3__sales" }, reply: { throws: "only SELECT or WITH queries are allowed" } },
  { name: "files query wrong type", servers: ["neko_files"], tool: "files_query_tables", arguments: { sql: 7 } },
  { name: "unknown tool", servers: ["neko_files"], tool: "files_delete", arguments: { path: "x" } },
  { name: "graphjin execute", servers: ["neko_memory", "neko_graphjin"], tool: "graphjin_execute_graphql", arguments: { query: "{ orders { id total } }", variables: { n: 2 } }, reply: { value: GQL_ROWS } },
  { name: "graphjin execute after list", servers: ["neko_graphjin"], listFirst: true, tool: "graphjin_execute_graphql", arguments: { query: "{ orders { id } }" }, reply: { value: GQL_ROWS } },
  { name: "graphjin no arguments", servers: ["neko_graphjin"], tool: "graphjin_graphql_help", reply: { value: { content: [{ type: "text", text: "help text" }] } } },
  { name: "graphjin empty arguments", servers: ["neko_graphjin"], tool: "graphjin_graphql_help", arguments: {}, reply: { value: { content: [{ type: "text", text: "help text" }] } } },
  { name: "graphjin error result", servers: ["neko_graphjin"], tool: "graphjin_execute_graphql", arguments: { query: "{ nope }" }, reply: { value: { isError: true, content: [{ type: "text", text: "unknown table nope" }] } } },
  { name: "graphjin result extra fields", servers: ["neko_graphjin"], tool: "graphjin_query_catalog", arguments: { ids: ["t1"] }, reply: { value: { structuredContent: { rows: [1] }, content: [{ type: "text", text: "{\"rows\":[1]}" }], _meta: { took: 3 }, extra: "kept" } } },
  { name: "graphjin result no content", servers: ["neko_graphjin"], tool: "graphjin_query_catalog", arguments: { ids: [] }, reply: { value: { structuredContent: { rows: [] } } } },
  { name: "graphjin call fails", servers: ["neko_graphjin"], tool: "graphjin_execute_graphql", arguments: { query: "{ orders { id } }" }, reply: { throws: "No data source is available to this run." } },
  { name: "graphjin agent tool hidden", servers: ["neko_graphjin"], tool: "graphjin_ask_graphjin_agent", arguments: { question: "why" }, reply: { value: GQL_ROWS } },
  { name: "graphjin agent tool listed by server", servers: ["neko_graphjin"], graphjinTools: [{ name: "ask_graphjin_agent", inputSchema: { type: "object" } }], tool: "graphjin_ask_graphjin_agent", arguments: { question: "why" }, reply: { value: GQL_ROWS } },
  { name: "graphjin unknown tool", servers: ["neko_graphjin"], tool: "graphjin_drop_table", arguments: {}, reply: { value: GQL_ROWS } },
  { name: "graphjin list fails", servers: ["neko_memory", "neko_graphjin"], listThrows: "GraphJin MCP tools/list failed: 503", tool: "graphjin_execute_graphql", arguments: { query: "{ orders { id } }" }, reply: { value: GQL_ROWS } },
  { name: "graphjin memory tool while list fails", servers: ["neko_memory", "neko_graphjin"], listThrows: "GraphJin MCP tools/list failed: 503", tool: "memory_search", arguments: { query: "revenue" }, reply: { value: [] } },
  { name: "policy execute", servers: ["neko_graphjin"], policy: "direct-governed", tool: "graphjin_execute_graphql", arguments: { query: "query { orders { id } }" }, reply: { value: GQL_ROWS } },
  { name: "policy blank query", servers: ["neko_graphjin"], policy: "direct-governed", tool: "graphjin_execute_graphql", arguments: { query: "   " }, reply: { value: GQL_ROWS } },
  { name: "policy query not text", servers: ["neko_graphjin"], policy: "direct-governed", tool: "graphjin_execute_graphql", arguments: { query: 5 }, reply: { value: GQL_ROWS } },
  { name: "policy saved query", servers: ["neko_graphjin"], policy: "direct-governed", tool: "graphjin_execute_saved_query", arguments: { name: "top" }, reply: { value: GQL_ROWS } },
  { name: "policy saved query after list", servers: ["neko_graphjin"], policy: "direct-governed", graphjinTools: [{ name: "execute_saved_query", inputSchema: { type: "object" } }, { name: "graphql_help", inputSchema: { type: "object" } }], tool: "graphjin_graphql_help", arguments: {}, reply: { value: GQL_ROWS } },
  { name: "no saved query listed", servers: ["neko_graphjin"], tool: "graphjin_execute_saved_query", arguments: { name: "top" }, reply: { value: GQL_ROWS } },
];

type ListCase = { name: string; servers: string[]; memoryReadOnly?: boolean; policy?: string; graphjinTools?: unknown[]; listThrows?: string };

const LISTS: ListCase[] = [
  { name: "memory, guides and files", servers: ["neko_memory", "neko_guides", "neko_files"] },
  { name: "read-only memory", servers: ["neko_memory"], memoryReadOnly: true },
  { name: "graphjin between memory and files", servers: ["neko_memory", "neko_graphjin", "neko_files"] },
  { name: "graphjin direct-governed", servers: ["neko_graphjin"], policy: "direct-governed" },
  { name: "graphjin list fails", servers: ["neko_memory", "neko_graphjin"], listThrows: "GraphJin MCP tools/list failed: 503" },
  { name: "graphjin lists nothing", servers: ["neko_graphjin"], graphjinTools: [] },
];

const METHODS = [
  "searchWorkMemoryByContext",
  "rememberWorkMemory",
  "updateWorkMemory",
  "searchSkillsForRun",
  "searchFilesForRun",
  "readFileForRun",
  "queryFileTablesForRun",
];

function fakeControlPlane(
  reply: Reply | undefined,
  calls: unknown[],
  graphjin: { graphjinTools?: unknown[]; listThrows?: string } = {},
): BrokerControlPlane {
  const plane: Record<string, (input: unknown) => Promise<unknown>> = {};
  for (const method of [...METHODS, "callGraphjinTool"]) {
    plane[method] = async (input) => {
      calls.push({ method, input });
      if (reply?.throws) throw new Error(reply.throws);
      return reply?.value;
    };
  }
  plane.listGraphjinTools = async (input) => {
    calls.push({ method: "listGraphjinTools", input });
    if (graphjin.listThrows) throw new Error(graphjin.listThrows);
    return graphjin.graphjinTools ?? GJ_TOOLS;
  };
  return plane as unknown as BrokerControlPlane;
}

async function connect(servers: string[], memoryReadOnly: boolean, controlPlane: BrokerControlPlane, policy?: string) {
  const server = await buildMultiplexedBridgeServer(servers, {
    runKind: "work",
    orgId: "11111111-1111-4111-8111-111111111111",
    threadId: "22222222-2222-4222-8222-222222222222",
    runId: "33333333-3333-4333-8333-333333333333",
    skillsRoot: "/tmp/skills",
    packActions: [],
    controlPlane,
    memoryReadOnly,
    ...(policy ? { graphjinToolPolicy: parseGraphjinMcpToolPolicy(policy) } : {}),
  });
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.instance.connect(serverTransport);
  const client = new Client({ name: "fixture", version: "1" });
  await client.connect(clientTransport);
  return client;
}

describe("tool server fixtures for the Go broker", () => {
  it("matches the fixture the Go test reads", async () => {
    const lists = [];
    for (const list of LISTS) {
      const client = await connect(list.servers, list.memoryReadOnly ?? false, fakeControlPlane(undefined, [], list), list.policy);
      lists.push({ ...list, tools: (await client.listTools()).tools });
      await client.close();
    }
    const calls = [];
    for (const call of CALLS) {
      const recorded: unknown[] = [];
      const client = await connect(call.servers, call.memoryReadOnly ?? false, fakeControlPlane(call.reply, recorded, call), call.policy);
      let outcome: Record<string, unknown>;
      try {
        if (call.listFirst) await client.listTools();
        outcome = { result: await client.callTool({ name: call.tool, ...(call.arguments ? { arguments: call.arguments } : {}) }) };
      } catch (err) {
        outcome = { error: err instanceof Error ? err.message : String(err) };
      }
      calls.push({ ...call, ...outcome, controlPlane: recorded });
      await client.close();
    }
    const fixture = `${JSON.stringify({ graphjinTools: GJ_TOOLS, lists, calls }, null, 1)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
