import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { buildMultiplexedBridgeServer } from "../../src/agent-sandbox/mcp-bridge.js";
import type { BrokerControlPlane } from "../../src/agent-sandbox/broker-client.js";
import { parseGraphjinMcpToolPolicy, type PackActionDescriptor } from "@neko/llm/sandbox-runtime";

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
  wantsCards?: boolean;
  cardSchema?: string;
  eventsFail?: boolean;
  workflowRunId?: string;
  observationId?: string;
  packActions?: PackActionDescriptor[];
  tool: string;
  arguments?: unknown;
  reply?: Reply;
  replies?: Record<string, Reply>;
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

const ASK = ["neko_interaction"];
const SKILLS = ["neko_skills"];
const UI = ["neko_ui"];
const SALES = "| Region | Revenue | Orders |\n|---|---:|---:|\n| North | 12699845.92 | 1,250 |\n| South | 9876543.2 | 980 |\n| East | -3.5 | 0 |";
const SMALL = "| Item | Price |\n| A | 1.005 |\n| B | 2.5 |\n| C | 0.125 |";
const WHAT_IF = {
  title: "Price what-if",
  controls: ["price_change: Price change (%) = 5 (-20 to 20, step 1)", "Plan: Plan = Basic (10) | Pro (25)", "volume: Volume = 0.5 (0 to 1)"],
  values: ["Road_Revenue = 1,496,791.67"],
  computed: ["New revenue (USD): road_revenue * (1 + price_change / 100)", "Per plan: round(road_revenue / plan, 2) + max(volume, abs(-1)) ^ 2"],
};

CALLS.push(
  { name: "ask one question", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "  Which region?  " }] } },
  { name: "ask with options and cards", servers: ASK, wantsCards: true, tool: "interaction_ask_user_question", arguments: { reason: " Totals differ by region. ", questions: [{ header: "Region", question: "Which region?", options: [{ label: "North", description: "EU stores" }, { label: "South" }] }] } },
  { name: "ask three questions with cards", servers: ASK, wantsCards: true, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "Which period?" }, { header: "Basis", question: "Gross or net?", options: [{ label: "Gross" }, { label: "Net" }, { label: "Both", description: "Show both" }] }, { header: "Currency", question: "Which currency?" }] } },
  { name: "ask two questions no cards", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "Which period?", options: [{ label: "Q1" }, { label: "Q2" }] }, { question: "Which store?" }] } },
  { name: "ask four questions", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "a" }, { question: "b" }, { question: "c" }, { question: "d" }] } },
  { name: "ask no questions", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [] } },
  { name: "ask missing questions", servers: ASK, tool: "interaction_ask_user_question", arguments: { reason: "why" } },
  { name: "ask questions not a list", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: "Which region?" } },
  { name: "ask question faults", servers: ASK, tool: "interaction_ask_user_question", arguments: { reason: "  ", questions: [{ zz: 1, question: " ", aa: 2, header: "h".repeat(41), options: [{ label: "", extra: true }] }, 7, null] } },
  { name: "ask six options", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "Pick", options: ["a", "b", "c", "d", "e", "f"].map((label) => ({ label })) }] } },
  { name: "ask option faults", servers: ASK, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "Pick", options: [{ label: "x".repeat(81), description: "" }, "y"] }] } },
  { name: "ask long question", servers: ASK, tool: "interaction_ask_user_question", arguments: { reason: "r".repeat(501), questions: [{ question: "€".repeat(501) }] } },
  { name: "ask events fail", servers: ASK, wantsCards: true, eventsFail: true, tool: "interaction_ask_user_question", arguments: { questions: [{ question: "Which region?" }] } },
  { name: "skill minimal", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "Refund Report!!", description: "  Weekly refunds.  ", body: "\n# Refunds\n\nRun it.\n\n" } },
  { name: "skill all fields", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "sales-pack", description: "Sales \"pack\"\nline two", body: "Body", license: " Apache-2.0 ", compatibility: "Needs python3", metadata: { "owner.team": "ops €", version: "1" }, allowedTools: "Bash Read", files: [{ path: "scripts/run.py", content: "print('hi')\n" }, { path: "\\docs\\a.md", content: "" }, { path: "//ref/b.txt", content: "€ text" }] } },
  { name: "skill long name", servers: SKILLS, tool: "skills_create_skill", arguments: { name: `${"a".repeat(63)} b`, description: "d", body: "b" } },
  { name: "skill name normalizes to nothing", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "!!!", description: "d", body: "b" } },
  { name: "skill blank body", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "blank", description: "d", body: "   " } },
  { name: "skill path escapes", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "escape", description: "d", body: "b", files: [{ path: "ok.txt", content: "1" }, { path: "a/../../x", content: "2" }] } },
  { name: "skill empty path part", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "parts", description: "d", body: "b", files: [{ path: "a//b", content: "2" }] } },
  { name: "skill faults", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "x".repeat(65), description: "   ", body: "", license: "", metadata: { " ": "v", ok: "x".repeat(2049), ["k".repeat(129)]: "v" }, files: [{ path: "", content: 5 }, { content: "c" }, "f"] } },
  { name: "skill too many files", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "many", description: "d", body: "b", files: Array.from({ length: 31 }, (_, i) => ({ path: `f${i}.txt`, content: "x" })) } },
  { name: "skill extra fields", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "extra", description: "d", body: "b", owner: "someone", files: [{ path: "a.txt", content: "a", mode: "755" }] } },
  { name: "skill metadata not text", servers: SKILLS, tool: "skills_create_skill", arguments: { name: "meta", description: "d", body: "b", metadata: { n: 1 }, allowedTools: ["Bash"] } },
  { name: "skill missing fields", servers: SKILLS, tool: "skills_create_skill", arguments: {} },
  { name: "cards full report", servers: UI, tool: "ui_render_cards", arguments: { title: " Revenue by region ", stage: "final", table: SALES, chart: "bar", chartColumn: "revenue", keyFigures: ["Total: $22.6M (up 4%)", "Regions: 3"], callout: "North leads.", mood: "good", followUps: ["Why is East negative?", "Show orders"] } },
  { name: "cards draft auto chart", servers: UI, tool: "ui_render_cards", arguments: { title: "Prices", stage: "draft", table: SMALL, chart: "line" } },
  { name: "cards escaped lines", servers: UI, tool: "ui_render_cards", arguments: { title: "Lines", table: "| A | B |\\n| x | 1 |\\n| y | 2 |", keyFigures: "One: 1\nTwo: 2 (note)\n\n", followUps: "Next?" } },
  { name: "cards nested arguments", servers: UI, tool: "ui_render_cards", arguments: { arguments: { title: "Nested", callout: "Check stock." } } },
  { name: "cards json text", servers: UI, tool: "ui_render_cards", arguments: { input: JSON.stringify({ title: "Json", callout: "From text", mood: "act" }) } },
  { name: "cards empty fields dropped", servers: UI, tool: "ui_render_cards", arguments: { title: "Sparse", table: null, chart: false, chartColumn: "", keyFigures: [], callout: "Only this.", extra: "x" } },
  { name: "cards rich fields when core", servers: UI, tool: "ui_render_cards", arguments: { title: "Core", callout: "x", layout: "compare", diagram: ["A -> B"] } },
  { name: "cards compare rich", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Plans", layout: "compare", table: "| Plan | Price |\n| Basic | 10 |\n| Pro | 25 |", rowDetails: ["Basic >> | Feature | Limit |\n| Users | 3 |", "Pro >> | Feature | Limit |\n| Users | 30000.5 |"], drillDown: "Tell me about {label}" } },
  { name: "cards what-if", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: WHAT_IF },
  { name: "cards what-if faults", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Bad", controls: ["a: A = 5 (10 to 1)", "b: B = 50 (0 to 10)", "c: C = 1 (0 to 2, step 0)", "nope", "d: D = X | Y (2)", "a: Again = 1 (0 to 2)"], values: ["e = 1", "bad"], computed: ["Out: a + zz", "Div: 1 / 0", "Parse: a +", "NoColon", "Fn: foo(1)", "Arity: abs(1, 2)", "Paren: (a", "Char: a $ 2"] } },
  { name: "cards what-if half", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Half", controls: ["a: A = 1 (0 to 2)"] } },
  { name: "cards what-if non-finite", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Inf", controls: ["a: A = 0 (0 to 2)"], values: ["one = 1"], computed: ["Pow: one ^ (1 / a)", "Ok: min(a, one, 3) + floor(1.5) + ceil(0.2) + round(2.5) + 1e400 - 1e400"] } },
  { name: "cards map and diagram", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Stores", map: ["Paris @ 48.85, 2.35: 1,200", "Lyon @45.76,4.84:300"], mapValue: "Sales (EUR)", diagram: ["Order -> Pick: 2h", "Pick -> Ship", "order -> Cancel", "Standalone"], drillDown: "Explain {label}" } },
  { name: "cards map and diagram faults", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Bad map", map: ["Nowhere @ 91, 0: 5", "Neg @ 1, 1: -5", "Text"], diagram: ["-> B", ...Array.from({ length: 31 }, (_, i) => `N${i}`)], drillDown: "no placeholder" } },
  { name: "cards tool", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Calc", tool: "<label>Qty <input id=q></label><output id=o></output><script>q.oninput=()=>o.value=q.value*2</script><a href=\"#top\">top</a><img src=\"data:image/png;base64,AA\">" } },
  { name: "cards tool reaches out", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Calc", tool: "<img src=' https://x.example/a.png'>" } },
  { name: "cards tool fetch", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Calc", tool: "<button onclick=\"fetch('/x')\">Go</button>" } },
  { name: "cards tool css url", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Calc", tool: "<div style=\"background: url( 'DATA:x')\">a</div><div style=\"background:url(http://x)\">b</div>" } },
  { name: "cards tool protocol relative", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Calc", tool: "<script>let a = 1;</script><link rel=x><p>hi</p>" } },
  { name: "cards table faults", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: "| A | B |\n| 1 |", chart: "bar" } },
  { name: "cards table one row", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: "| A | B |\n|---|---|" } },
  { name: "cards chart column faults", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: SALES, chart: "bar", chartColumn: "Region" } },
  { name: "cards chart no numbers", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: "| A | B |\n| x | y |\n| z | 1 |", chart: "area" } },
  { name: "cards chart bad value", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: "| A | B |\n| x | 1 |\n| z | n/a |", chart: "area", chartColumn: "b" } },
  { name: "cards chart one point", servers: UI, tool: "ui_render_cards", arguments: { title: "Bad", table: "| A | B |\n| x | 1 |", chart: "line" } },
  { name: "cards donut", servers: UI, tool: "ui_render_cards", arguments: { title: "Mix", table: SALES, chart: "donut", chartColumn: "Orders" } },
  { name: "cards donut negative", servers: UI, tool: "ui_render_cards", arguments: { title: "Mix", table: SALES, chart: "donut", chartColumn: "Revenue" } },
  { name: "cards compare faults", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Cmp", layout: "compare", table: "| A | B |\n| x | 1 |", rowDetails: ["y >> | C |\n| 1 |", "x >> | C |"], chart: "pie" } },
  { name: "cards layout without table", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "Cmp", layout: "compare", chart: "bar", rowDetails: ["x >> | C |\n| 1 |"], callout: "c" } },
  { name: "cards empty answer", servers: UI, tool: "ui_render_cards", arguments: { title: "Nothing", stage: "draft" } },
  { name: "cards schema faults", servers: UI, tool: "ui_render_cards", arguments: { title: "  ", stage: "done", keyFigures: ["Bad", "x".repeat(161), 3, "A: 1", "B: 2", "C: 3", "D: 4"], callout: 5, mood: "calm", followUps: true } },
  { name: "cards key figure faults", servers: UI, tool: "ui_render_cards", arguments: { title: "KF", keyFigures: ["Label:", ": 5", "Total: (only note)", "Margin: 12% (vs 10%) (est)"] } },
  { name: "cards missing title", servers: UI, tool: "ui_render_cards", arguments: { callout: "x" } },
  { name: "cards not an object", servers: UI, tool: "ui_render_cards", arguments: { only: [1, 2] } },
  { name: "cards no arguments", servers: UI, tool: "ui_render_cards" },
  { name: "cards number formats", servers: UI, tool: "ui_render_cards", arguments: { title: "Fmt", table: "| N | Big | Mixed | Neg | Zero |\n| a | 12345.678 | 1.5 | -0.004 | -0 |\n| b | 1e21 | 9999.995 | -1,234.5 | 0 |\n| c | 0x10 | 2 | 1. | .5 |" } },
  { name: "cards events fail", servers: UI, eventsFail: true, tool: "ui_render_cards", arguments: { title: "Fail", callout: "x" } },
  { name: "cards tool javascript link", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<a href=\"javascript:alert(1)\">x</a>" } },
  { name: "cards tool hash and data", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<a href = '#x'>x</a><img srcset=\" data:image/png;base64,AA\"><p data-x=\"1\">y</p>" } },
  { name: "cards tool protocol-relative src", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<img src = '//cdn.example/a.png'>" } },
  { name: "cards tool window open", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<script type=module>\nwindow . open('x')\n</script >" } },
  { name: "cards tool handler import", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<body onload=import('x')>" } },
  { name: "cards tool location outside script", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<p>location.href and fetch are words here</p>" } },
  { name: "cards tool upper iframe", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "< IFRAME></iframe>" } },
  { name: "cards choice faults", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "F", controls: ["c: C = One (1) | Two (2) | Three (x)", `d: D = ${Array.from({ length: 13 }, (_, i) => `O${i} (${i})`).join(" | ")}`], computed: ["Out: 1"] } },
  { name: "cards tool upper data attr", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<object DATA=\"https://x\"></object>" } },
  { name: "cards tool safe script", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "T", tool: "<script>const total = [1,2].reduce((a, b) => a + b); document.getElementById('o').textContent = total; const reopen = 1;</script><output id=o></output>" } },
  { name: "cards formula faults", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "F", controls: ["a: A = 2 (0 to 4)"], values: ["b = 3"], computed: ["Two numbers: 1.50 2.50", "Empty:   ", "No args: max()", "Many: min(1,2,3,4,5,6,7,8,9)", "Close: abs(1", "Op: a * * b"] } },
  { name: "cards formula forms", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "F", controls: ["a: A = 2 (0 to 12.5)", "b: B (kg) = 0.05 (0 to 0.07)", "c: C = 1 (-3 to 997)"], computed: ["Right: a ^ b ^ 2", "Neg: -a ^ 2 + +b", "Round: round(a * 1234.5, -1) + round(c)", "Exp: 1.5e3 * .5 + 5. - A", "Unit (a very long unit text here): a"] } },
  { name: "cards control odd numbers", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "F", controls: ["a: A = 1.2.3 (0 to 2)", "b: B = 1 (0 to 2, step 1.2.3)"], computed: ["Out: b"] } },
  { name: "cards control unused default", servers: UI, cardSchema: "rich", tool: "ui_render_cards", arguments: { title: "F", controls: ["a: A = 1.2.3 (0 to 2)", "b: B = 1 (0 to 2)"], computed: ["Out: b"] } },
  { name: "cards and ask together", servers: ["neko_interaction", "neko_ui", "neko_skills"], wantsCards: true, tool: "ui_render_cards", arguments: { title: "Both", callout: "x" } },
);

const WF_RUN = "66666666-6666-4666-8666-666666666666";
const OBSERVATION = "77777777-7777-4777-8777-777777777777";
const POLICY = { id: "88888888-8888-4888-8888-888888888888", name: "slack_auto", mode: "auto_approve", priority: 100 };
const ALLOW = { decision: "allow", policy: POLICY, mode: "auto_approve" };
const NEEDS_APPROVAL = { decision: "needs_approval", policy: { ...POLICY, name: "external_default" }, mode: "approval_required", reason: "policy \"external_default\" mode=approval_required" };
const DENY = { decision: "deny", policy: { ...POLICY, name: "no_email" }, mode: "never", reason: "policy \"no_email\" mode=never" };
const NONE = { decision: "no_policy", reason: "no_matching_policy" };
const REQ = "99999999-9999-4999-8999-999999999999";
const APPROVED = { id: REQ, status: "approved" };
const PENDING = { id: REQ, status: "pending_approval" };
const ACTION = ["neko_action"];
const OUTPUT = ["neko_workflow_output"];
const PACK = ["neko_pack_actions"];
const SLACK_ARGS = { scope: "external", kind: "slack_post", target: "C123", payload: { text: "Stock is low", nested: { a: [1, null] } }, risk_level: "low", summary: "Post the low stock list to #ops", minutes_saved: 8, basis: "Typing the list by hand" };
const PACK_ACTIONS: PackActionDescriptor[] = [
  { kind: "magento_reindex", description: "Reindex a Magento index. Payload schema: {\"type\":\"object\"}.", default_mode: "ask", example: { index: "catalog" } },
  { kind: "slack_post", description: "Post a Slack message.", scope: "internal", default_mode: "auto" },
  { kind: "crm_update", description: "Update a CRM record.", default_mode: { external: "auto", internal: "ask" } },
  { kind: "crm_note", description: "Add a CRM note.", default_mode: { external: "auto" } },
  { kind: "ledger_sync", description: "Sync the ledger." },
  { kind: "drop_all", description: "Never offered.", default_mode: "deny" },
  { kind: "drop_some", description: "Never offered either.", default_mode: { external: "deny", internal: "deny" } },
  { kind: "half_deny", description: "Denied outside.", default_mode: { external: "deny", internal: "auto" } },
];
const RAN = { status: "succeeded", outcome: { result: { ts: "1700000000.1" }, externalRef: "C123/1700000000.1", commandOrOperation: "chat.postMessage" } };
const OUT = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", orgId: "11111111-1111-4111-8111-111111111111", kind: "finding", title: "Low stock", seenCount: 1, createdAt: "2026-11-13T00:00:00.000Z" };
const pack = (name: string, tool: string, args: unknown, replies: Record<string, Reply>, extra: Partial<CallCase> = {}): CallCase =>
  ({ name, servers: PACK, packActions: PACK_ACTIONS, tool, arguments: args, replies, ...extra });

CALLS.push(
  { name: "action auto approved", servers: ACTION, workflowRunId: WF_RUN, observationId: OBSERVATION, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED } } },
  { name: "action needs approval", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: { ...SLACK_ARGS, minutes_saved: 600 }, replies: { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } } },
  { name: "action allowed but held by preflight", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: { scope: "internal", kind: " slack_post ", summary: "  Post it  " }, replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: PENDING } } },
  { name: "action denied", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: DENY } } },
  { name: "action no policy", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: NONE } } },
  { name: "action policy fails", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { throws: "policy store is down" } } },
  { name: "action create fails", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { throws: "action slack_post is not available to this run" } } },
  { name: "action enqueue fails", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, enqueueActionExecute: { throws: "the job queue is not ready" } } },
  { name: "action events fail", servers: ACTION, workflowRunId: WF_RUN, eventsFail: true, tool: "action_request", arguments: SLACK_ARGS, replies: { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } } },
  { name: "action argument faults", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: { scope: "outside", kind: "  ", target: "", payload: [1], risk_level: "huge", minutes_saved: 1.5, basis: "b".repeat(161), extra: true } },
  { name: "action missing fields", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: {} },
  { name: "action minutes out of range", servers: ACTION, workflowRunId: WF_RUN, tool: "action_request", arguments: { ...SLACK_ARGS, minutes_saved: -1, summary: "s".repeat(2001), kind: 7 } },
  { name: "output full", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "finding", title: "Low stock", body: "Three items", payload: { skus: ["A", "B"] }, artifactPath: "out/low.csv", scope: "inventory", topic: "stock", mood: "act", timeWindowStart: "2026-11-01T00:00Z", timeWindowEnd: "2026-11-13T09:30:15.1239Z", freshnessTtlSeconds: 3600 }, replies: { emitWorkflowOutput: { value: OUT } } },
  { name: "output kind only", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "summary" }, replies: { emitWorkflowOutput: { value: { ...OUT, kind: "summary" } } } },
  { name: "output whole seconds", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "report", timeWindowStart: "2024-02-29T23:59:59Z", timeWindowEnd: "2026-11-13T09:30:15.5Z" }, replies: { emitWorkflowOutput: { value: OUT } } },
  { name: "output fails", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "finding" }, replies: { emitWorkflowOutput: { throws: "workflow run not found" } } },
  { name: "output events fail", servers: OUTPUT, workflowRunId: WF_RUN, eventsFail: true, tool: "workflow_output_emit", arguments: { kind: "finding" }, replies: { emitWorkflowOutput: { value: OUT } } },
  { name: "output argument faults", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "memo", title: "t".repeat(241), payload: "x", mood: "bad", timeWindowStart: "2026-11-01", timeWindowEnd: "2026-11-01T00:00:00+02:00", freshnessTtlSeconds: 0 } },
  { name: "output date faults", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "finding", timeWindowStart: "2027-02-29T00:00:00Z", timeWindowEnd: 5, freshnessTtlSeconds: 1.5, scope: 3 } },
  { name: "output ttl too big", servers: OUTPUT, workflowRunId: WF_RUN, tool: "workflow_output_emit", arguments: { kind: "finding", freshnessTtlSeconds: 31_536_001, topic: "t".repeat(121) } },
  pack("pack auto runs", "pack_actions_slack_post", { target: "C123", risk_level: "low", payload: { text: "hi" } }, { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, waitForActionExecution: { value: RAN } }),
  pack("pack auto fails", "pack_actions_slack_post", { payload: { text: "hi" } }, { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, waitForActionExecution: { value: { status: "failed", error: "channel_not_found" } } }),
  pack("pack auto still running", "pack_actions_slack_post", { intent: "Tell ops", target: "" }, { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, waitForActionExecution: { value: { status: "timeout" } } }),
  pack("pack ask", "pack_actions_magento_reindex", { intent: "Reindex the catalog so prices show", payload: { index: "catalog" }, risk_level: "medium" }, { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } }),
  pack("pack ask no risk", "pack_actions_magento_reindex", { intent: "Reindex the catalog" }, { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } }),
  pack("pack ask without intent", "pack_actions_magento_reindex", { payload: {} }, {}),
  pack("pack auto escalated", "pack_actions_slack_post", { target: "lead/7" }, { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } }),
  pack("pack denied", "pack_actions_crm_note", { intent: "Note it" }, { evaluateActionPolicy: { value: DENY } }),
  pack("pack no policy", "pack_actions_ledger_sync", {}, { evaluateActionPolicy: { value: NONE } }),
  pack("pack half denied", "pack_actions_half_deny", {}, { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, waitForActionExecution: { value: RAN } }),
  pack("pack denied kind hidden", "pack_actions_drop_all", {}, { evaluateActionPolicy: { value: ALLOW } }),
  pack("pack argument faults", "pack_actions_crm_update", { intent: "no", target: 5, risk_level: "none", payload: [] }, {}),
  pack("pack wait fails", "pack_actions_slack_post", {}, { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED }, waitForActionExecution: { throws: "broker /v1/action/wait unreachable" } }),
  pack("pack create fails", "pack_actions_magento_reindex", { intent: "Reindex the catalog" }, { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { throws: "action preflight failed: store offline" } }),
  pack("pack events fail", "pack_actions_magento_reindex", { intent: "Reindex the catalog" }, { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } }, { eventsFail: true }),
);

const WF_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const SUB_ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const RULE_ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
const BUILDERS = ["neko_workflow_builder"];
const RULES = ["neko_rule_builder"];
const STEPS = [{ id: "check", description: "Check stock levels" }, { id: "post", description: "Post the list" }];
const WORKFLOW = { id: WF_ID, orgId: "11111111-1111-4111-8111-111111111111", name: "Stock check", description: "Daily stock check", enabled: true, status: "active", goal: "Keep stock up", systemPromptOverlay: "", steps: STEPS, cron: "0 9 * * *", cronTimezone: "Europe/Paris", cronEnabled: true, networkHosts: [], dailyRunBudget: null, outputContract: null, ownerUserId: "", createdAt: "2026-11-01T00:00:00.000Z", updatedAt: "2026-11-12T00:00:00.000Z" };
const SUBSCRIPTION = { id: SUB_ID, workflowId: WF_ID, sourceKind: "source_change", filter: { table: "products", where: { qty: { lt: 5 }, active: { eq: true } }, primary_key: ["id", "sku"] }, enabled: true, idempotencyKeyTemplate: "{id}-{sku}" };
const BATCH_CONTRACT = { apiBatch: { version: 1, compiled: true, compiler: "workflow", recordsField: "orders", columns: [{ name: "Order", path: "order.id" }, { name: "Total", path: "total", default: 0 }] } };
const RULE = { id: RULE_ID, orgId: "11111111-1111-4111-8111-111111111111", name: "slack_auto", description: "Auto-post to Slack", appliesToKinds: ["slack_post", "slack_dm"], appliesToScopes: ["internal", "external"], mode: "auto_approve", riskThresholdAutoApprove: "low", allowedTargets: { patterns: ["C*"], on_miss: "next" }, deniedTargets: null, limits: { perDay: 20 }, approverGroupId: null, priority: 50, enabled: true, createdByThreadId: null, createdByRunId: null, createdAt: "2026-11-01T00:00:00.000Z", updatedAt: "2026-11-02T00:00:00.000Z" };
const FULL_WORKFLOW = {
  name: "  Stock check  ",
  description: "Daily stock check",
  goal: "Keep stock up",
  systemPromptOverlay: "Be brief.",
  steps: [{ id: " check ", description: " Check stock levels ", extra: 1 }, { id: "run", description: "Run the export", script: { skill: "stock-export", command: ["python3", "${SKILL_DIR}/run.py", "${input.sku:-all}"], timeoutSeconds: 60 } }],
  batch: { recordsField: " orders ", columns: [{ name: "Order", path: "order.id" }, { name: "Total", path: "total", default: 0 }, { name: "Note", path: "note", default: null }] },
  triggers: { cron: " 0 9 * * * ", timezone: "Europe/Paris", enabled: true, when: { table: " products ", where: { qty: { lt: 5 } }, select: ["id", "qty"], primary_key: ["id"], version_column: "updated_at", enabled: false, idempotency_key_template: "{id}", acknowledge_mutation_loop: true }, watch: { query: " { products { count } } ", value_path: "products.0.count", op: "lt", threshold: 5, cadence_seconds: 600, debounce_seconds: 0, severity: "high" } },
};

CALLS.push(
  { name: "workflow create", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS }, replies: { saveWorkflowWithTrigger: { value: { action: "created", workflow: { ...WORKFLOW, cron: null, description: "" } } } } },
  { name: "workflow full update with trigger", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: FULL_WORKFLOW, replies: { saveWorkflowWithTrigger: { value: { action: "updated", workflow: WORKFLOW, subscription: SUBSCRIPTION } } } },
  { name: "workflow trigger with no filter", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS, batch: null, triggers: { when: { table: "products", primary_key: ["id"] } } }, replies: { saveWorkflowWithTrigger: { value: { action: "created", workflow: WORKFLOW, subscription: { ...SUBSCRIPTION, filter: { table: "products", primary_key: ["id"], where: {} }, idempotencyKeyTemplate: null } } } } },
  { name: "workflow trigger odd filter", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS }, replies: { saveWorkflowWithTrigger: { value: { action: "created", workflow: { ...WORKFLOW, cronTimezone: "UTC" }, subscription: { ...SUBSCRIPTION, filter: { table: 7, primary_key: "id" } } } } } },
  { name: "workflow trigger error", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS, triggers: { when: { table: "products", primary_key: ["id"] } } }, replies: { saveWorkflowWithTrigger: { value: { action: "updated", workflow: WORKFLOW, triggerError: { code: "mutation_loop", message: "loops" } } } } },
  { name: "workflow save fails", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS }, replies: { saveWorkflowWithTrigger: { throws: "A workflow named \"Stock check\" exists and this run cannot change it. Choose another name." } } },
  { name: "workflow events fail", servers: BUILDERS, eventsFail: true, tool: "workflow_builder_create_workflow", arguments: { name: "Stock check", steps: STEPS }, replies: { saveWorkflowWithTrigger: { value: { action: "created", workflow: WORKFLOW } } } },
  { name: "workflow argument faults", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "  ", description: 5, steps: [{ id: "", description: "d".repeat(2001), script: { skill: "Bad Skill", command: [], timeoutSeconds: 0.5 } }, "x"], batch: { recordsField: "1st", columns: [{ name: "a\nb", path: "" }, { name: "c", path: "p", default: [1] }] }, triggers: { cron: "", when: { table: "t", primary_key: [] }, watch: { query: "q", value_path: "", op: "between", threshold: true, cadence_seconds: 30, debounce_seconds: 604801, severity: "urgent" } } } },
  { name: "workflow missing fields", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: {} },
  { name: "workflow wrong types", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: 1, steps: "one", batch: "b", triggers: [] } },
  { name: "workflow nested wrong types", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "n", steps: [{ id: "a", description: "b", script: "s" }], batch: { columns: [] }, triggers: { enabled: "yes", when: "w", watch: { query: 5, value_path: "a", op: "gt", threshold: null } } } },
  { name: "workflow long name and too many steps", servers: BUILDERS, tool: "workflow_builder_create_workflow", arguments: { name: "n".repeat(121), steps: Array.from({ length: 41 }, (_, i) => ({ id: `s${i}`, description: "d" })), triggers: { when: { table: "t", primary_key: ["a", "b", "c", "d", "e", "f", "g", "h", "i"], where: [], select: [""] } } } },
  { name: "workflow list", servers: BUILDERS, tool: "workflow_builder_list_workflows", arguments: { limit: 2 }, replies: { listWorkflowsWithTriggers: { value: { total: 3, workflows: [{ ...WORKFLOW, outputContract: BATCH_CONTRACT, when: SUBSCRIPTION.filter }, { ...WORKFLOW, id: SUB_ID, name: "Other", outputContract: { apiBatch: { version: 2 } }, when: null }] } } } },
  { name: "workflow list default limit", servers: BUILDERS, tool: "workflow_builder_list_workflows", arguments: {}, replies: { listWorkflowsWithTriggers: { value: { total: 0, workflows: [] } } } },
  { name: "workflow list sparse rows", servers: BUILDERS, tool: "workflow_builder_list_workflows", arguments: {}, replies: { listWorkflowsWithTriggers: { value: { total: 1, workflows: [{ id: WF_ID, name: "Bare", goal: null, outputContract: { apiBatch: { version: 1, compiled: true, compiler: "workflow", recordsField: "9x", columns: [{ name: "A", path: "a.b", default: "z", extra: 1 }] } } }] } } } },
  { name: "workflow list fails", servers: BUILDERS, tool: "workflow_builder_list_workflows", arguments: { limit: 5 }, replies: { listWorkflowsWithTriggers: { throws: "database is down" } } },
  { name: "workflow list bad limit", servers: BUILDERS, tool: "workflow_builder_list_workflows", arguments: { limit: 201 } },
  { name: "workflow delete", servers: BUILDERS, tool: "workflow_builder_delete_workflow", arguments: { workflowId: WF_ID }, replies: { deleteWorkflow: { value: { found: true, name: "Stock check" } } } },
  { name: "workflow delete not found", servers: BUILDERS, tool: "workflow_builder_delete_workflow", arguments: { workflowId: "nope" }, replies: { deleteWorkflow: { value: { found: false, name: null } } } },
  { name: "workflow delete raced", servers: BUILDERS, tool: "workflow_builder_delete_workflow", arguments: { workflowId: WF_ID }, replies: { deleteWorkflow: { value: { found: true, name: null } } } },
  { name: "workflow delete events fail", servers: BUILDERS, eventsFail: true, tool: "workflow_builder_delete_workflow", arguments: { workflowId: WF_ID }, replies: { deleteWorkflow: { value: { found: true, name: "Stock check" } } } },
  { name: "workflow delete empty id", servers: BUILDERS, tool: "workflow_builder_delete_workflow", arguments: { workflowId: "" } },
  { name: "rule save minimal", servers: RULES, tool: "rule_builder_save_rule", arguments: { name: " slack_auto ", applies_to_kinds: [" slack_post "], mode: "auto_approve" }, replies: { upsertActionPolicyByName: { value: { action: "created", policy: { ...RULE, description: "", appliesToKinds: ["slack_post"], appliesToScopes: ["external"], priority: 100 } } } } },
  { name: "rule save full", servers: RULES, tool: "rule_builder_save_rule", arguments: { name: "slack_auto", description: "Auto-post to Slack", applies_to_kinds: [], applies_to_scopes: ["internal", "external"], mode: "approval_required", risk_threshold_auto_approve: "low", allowed_targets: { patterns: ["C*"], on_miss: "next" }, denied_targets: { patterns: ["C9"] }, limits: { perDay: 20 }, approver_role: "admin", priority: 0, enabled: false }, replies: { upsertActionPolicyByName: { value: { action: "updated", policy: { ...RULE, appliesToKinds: [], appliesToScopes: [] } } } } },
  { name: "rule save other role", servers: RULES, tool: "rule_builder_save_rule", arguments: { name: "r", applies_to_kinds: ["a"], mode: "never", approver_role: " finance " }, replies: { upsertActionPolicyByName: { value: { action: "created", policy: RULE } } } },
  { name: "rule save fails", servers: RULES, tool: "rule_builder_save_rule", arguments: { name: "r", applies_to_kinds: [], mode: "never" }, replies: { upsertActionPolicyByName: { throws: "built-in group administrators is missing" } } },
  { name: "rule save events fail", servers: RULES, eventsFail: true, tool: "rule_builder_save_rule", arguments: { name: "r", applies_to_kinds: [], mode: "never" }, replies: { upsertActionPolicyByName: { value: { action: "created", policy: RULE } } } },
  { name: "rule save argument faults", servers: RULES, tool: "rule_builder_save_rule", arguments: { name: "", applies_to_kinds: "slack", applies_to_scopes: ["outside"], mode: "maybe", risk_threshold_auto_approve: "none", allowed_targets: [], limits: null, priority: 10001, enabled: "yes", approver_role: 1 } },
  { name: "rule save missing fields", servers: RULES, tool: "rule_builder_save_rule", arguments: {} },
  { name: "rule list", servers: RULES, tool: "rule_builder_list_rules", arguments: { limit: 10 }, replies: { listActionPolicies: { value: { total: 2, policies: [RULE, { id: SUB_ID, name: "bare" }] } } } },
  { name: "rule list default limit", servers: RULES, tool: "rule_builder_list_rules", arguments: {}, replies: { listActionPolicies: { value: { total: 0, policies: [] } } } },
  { name: "rule list fails", servers: RULES, tool: "rule_builder_list_rules", arguments: {}, replies: { listActionPolicies: { throws: "database is down" } } },
  { name: "rule list bad limit", servers: RULES, tool: "rule_builder_list_rules", arguments: { limit: 0.5 } },
);

const USERS = ["neko_user_manager"];
const CHANNELS = ["neko_channel_manager"];
const SOURCES = ["neko_data_source_manager"];
const AUDIT = ["neko_audit"];
const USER_ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee";
const GROUP_ID = "ffffffff-ffff-4fff-8fff-ffffffffffff";
const USER_LIST = { users: [{ id: USER_ID, email: "ana@example.com", name: "Ana", role: "admin", disabledAt: null, lastLoginAt: "2026-11-12T08:00:00.000Z", groupIds: [] }], groups: [] };
const GROUP_LIST = { groups: [{ id: GROUP_ID, slug: "finance", name: "Finance", kind: "custom", memberCount: 1, members: [{ userId: USER_ID, email: "ana@example.com", sources: ["local"] }], grants: [{ itemType: "skill", itemId: "*" }], dataAccess: [] }], idpRules: [], groupDataAccessEnabled: false };
const ADMIN_ALLOW = { replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { value: APPROVED } } };
const ADMIN_ASK = { replies: { evaluateActionPolicy: { value: NEEDS_APPROVAL }, createActionRequest: { value: PENDING } } };
const FILTER = { or: [{ column: "region", op: "in", value: ["EU", "US"] }, { and: [{ column: "owner_id", op: "eq", value: { var: "user_id" } }, { column: "closed", op: "is_null", value: true }] }] };
const admin = (name: string, servers: string[], tool: string, args: unknown, extra: Partial<CallCase> = {}): CallCase => ({ name, servers, tool, arguments: args, ...extra });

CALLS.push(
  admin("users list", USERS, "user_manager_list_users", {}, { replies: { listUsers: { value: USER_LIST } } }),
  admin("users list no arguments", USERS, "user_manager_list_users", undefined, { replies: { listUsers: { value: { users: [] } } } }),
  admin("users list fails", USERS, "user_manager_list_users", {}, { replies: { listUsers: { throws: "database is down" } } }),
  admin("user invite auto", USERS, "user_manager_request_user_change", { action: "invite", email: " ben@example.com ", role: "member", intent: " Ben joins finance. " }, ADMIN_ALLOW),
  admin("user set role needs approval", USERS, "user_manager_request_user_change", { action: "set_role", userId: USER_ID, role: "admin", intent: "Ana runs the rules now." }, ADMIN_ASK),
  admin("user deactivate denied", USERS, "user_manager_request_user_change", { action: "deactivate", userId: USER_ID, intent: "Ana left." }, { replies: { evaluateActionPolicy: { value: DENY } } }),
  admin("user reactivate no policy", USERS, "user_manager_request_user_change", { action: "reactivate", userId: USER_ID, intent: "Ana is back." }, { replies: { evaluateActionPolicy: { value: NONE } } }),
  admin("user change denied without reason", USERS, "user_manager_request_user_change", { action: "deactivate", userId: USER_ID, intent: "Ana left." }, { replies: { evaluateActionPolicy: { value: { decision: "deny", policy: POLICY, mode: "never" } } } }),
  admin("user invite needs role", USERS, "user_manager_request_user_change", { action: "invite", email: "ben@example.com", intent: "Ben joins." }),
  admin("user set role needs user", USERS, "user_manager_request_user_change", { action: "set_role", role: "admin", intent: "Promote." }),
  admin("user change argument faults", USERS, "user_manager_request_user_change", { action: "delete", email: "not an email", userId: " ", role: "owner", intent: "x".repeat(501) }),
  admin("user change missing fields", USERS, "user_manager_request_user_change", {}),
  admin("user change email forms", USERS, "user_manager_request_user_change", { action: "invite", email: "a..b@example.com", role: "member", intent: "i" }),
  admin("user change email plus", USERS, "user_manager_request_user_change", { action: "invite", email: "first.last+tag@sub.example.co", role: "member", intent: "i", extra: 1 }, ADMIN_ASK),
  admin("user change request fails", USERS, "user_manager_request_user_change", { action: "deactivate", userId: USER_ID, intent: "Ana left." }, { replies: { evaluateActionPolicy: { value: ALLOW }, createActionRequest: { throws: "worker action preflight failed with status 500" } } }),
  admin("user change events fail", USERS, "user_manager_request_user_change", { action: "deactivate", userId: USER_ID, intent: "Ana left." }, { ...ADMIN_ALLOW, eventsFail: true }),
  admin("groups list", USERS, "user_manager_list_groups", {}, { replies: { listGroups: { value: GROUP_LIST } } }),
  admin("group create", USERS, "user_manager_request_group_change", { action: "create_group", name: " Finance ", description: "", intent: "A group for finance." }, ADMIN_ASK),
  admin("group add member", USERS, "user_manager_request_group_change", { intent: "Ana joins.", userId: USER_ID, groupId: GROUP_ID, action: "add_member", ruleId: "r1" }, ADMIN_ALLOW),
  admin("group idp rule", USERS, "user_manager_request_group_change", { action: "add_idp_rule", ssoGroupId: "s1", groupId: GROUP_ID, intent: "Map the SSO group." }, ADMIN_ASK),
  admin("group remove idp rule", USERS, "user_manager_request_group_change", { action: "remove_idp_rule", ruleId: "r1", intent: "Unmap." }, ADMIN_ASK),
  admin("group delete target", USERS, "user_manager_request_group_change", { action: "delete_group", groupId: GROUP_ID, name: "Finance", intent: "Gone." }, ADMIN_ASK),
  admin("group member needs two", USERS, "user_manager_request_group_change", { action: "remove_member", intent: "Out." }),
  admin("group create needs name", USERS, "user_manager_request_group_change", { action: "create_group", name: "", intent: "x" }),
  admin("group change argument faults", USERS, "user_manager_request_group_change", { action: "rename", name: "n".repeat(121), description: 5, intent: "" }),
  admin("item grant", USERS, "user_manager_request_item_grant", { action: "grant_item", groupId: GROUP_ID, itemType: "skill", itemId: "*", intent: "Finance uses every skill." }, ADMIN_ASK),
  admin("item grant faults", USERS, "user_manager_request_item_grant", { action: "grant", groupId: "", itemType: "report", itemId: "i".repeat(501) }),
  admin("data access set", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "sales.orders", columns: ["id", " total "], rowFilter: FILTER, intent: "Finance reads its orders." }, ADMIN_ASK),
  admin("data access remove", USERS, "user_manager_request_data_access_change", { action: "remove_table_access", ruleId: "r9", intent: "Drop it." }, ADMIN_ASK),
  admin("data access needs columns", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", intent: "x" }),
  admin("data access remove needs rule", USERS, "user_manager_request_data_access_change", { action: "remove_table_access", intent: "x" }),
  admin("data access empty and", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { and: [] }, intent: "x" }),
  admin("data access empty column", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { column: "", op: "eq", value: 1 }, intent: "x" }),
  admin("data access bad op", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { column: "a", op: "like", value: 1 }, intent: "x" }),
  admin("data access bad var", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { column: "a", op: "eq", value: { var: "email" } }, intent: "x" }),
  admin("data access nested fault", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { or: [{ column: "a", op: "eq", value: null }] }, intent: "x" }),
  admin("data access filter not object", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: [], rowFilter: "region = 'EU'", intent: "x" }),
  admin("data access extra filter keys", USERS, "user_manager_request_data_access_change", { action: "set_table_access", groupId: GROUP_ID, source: "erp", table: "orders", columns: ["id"], rowFilter: { and: [{ column: "a", op: "eq", value: 1, note: "n" }], or: [] }, intent: "x" }, ADMIN_ASK),
  admin("channels list", CHANNELS, "channel_manager_list_channels", {}, { replies: { listChannels: { value: { workspaces: [{ channelPlugin: "slack", workspaceId: "T1", createdAt: null }], identities: [] } } } }),
  admin("channel link", CHANNELS, "channel_manager_request_channel_change", { action: "link", identityId: " ci-1 ", appUserId: USER_ID, intent: "Ana uses Slack." }, ADMIN_ASK),
  admin("channel block auto", CHANNELS, "channel_manager_request_channel_change", { action: "block", identityId: "ci-1", appUserId: "", intent: "Spam." }, ADMIN_ALLOW),
  admin("channel link needs user", CHANNELS, "channel_manager_request_channel_change", { action: "link", identityId: "ci-1", intent: "x" }),
  admin("channel change faults", CHANNELS, "channel_manager_request_channel_change", { action: "mute", intent: "x" }),
  admin("sources list", SOURCES, "data_source_manager_list_data_sources", {}, { replies: { listDataSources: { value: { sources: [{ id: GROUP_ID, name: "erp", label: null, kind: "graphjin", authMode: "service", isDefault: true, enabled: true, host: "db.internal", hasMcp: false }] } } } }),
  admin("source register", SOURCES, "data_source_manager_request_data_source_change", { action: "register", name: " erp-2 ", label: " ERP two ", sourceKind: "database", intent: "A second ERP." }, ADMIN_ASK),
  admin("source enable empty label", SOURCES, "data_source_manager_request_data_source_change", { action: "enable", name: "erp", label: "", intent: "On." }, ADMIN_ALLOW),
  admin("source bad name", SOURCES, "data_source_manager_request_data_source_change", { action: "remove", name: "ERP_1", sourceKind: "sheet", intent: "x" }),
  admin("source long name", SOURCES, "data_source_manager_request_data_source_change", { action: "remove", name: "-" + "a".repeat(64), intent: "x" }),
  admin("audit trail", AUDIT, "audit_audit_trail", { limit: 20 }, { replies: { listAuditTrail: { value: { requests: [], alerts: [], gatewaySummary: [{ runId: null, backend: "openneko", actorRole: "admin", calls: 3 }] } } } }),
  admin("audit trail default", AUDIT, "audit_audit_trail", {}, { replies: { listAuditTrail: { value: { requests: [] } } } }),
  admin("audit trail denied", AUDIT, "audit_audit_trail", {}, { replies: { listAuditTrail: { value: { denied: true } } } }),
  admin("audit trail bad limit", AUDIT, "audit_audit_trail", { limit: 201 }),
  admin("audit trail fails", AUDIT, "audit_audit_trail", { limit: 1 }, { replies: { listAuditTrail: { throws: "database is down" } } }),
);

const SOURCE_CONFIG = ["neko_source_config_manager"];
const DATA_SOURCE_ID = "99999999-9999-4999-8999-999999999999";
const SPEC_ASSET = { id: "88888888-8888-4888-8888-888888888888", originalName: "orders.yaml", sourceType: "url", title: "Orders", version: "1.0", baseUrl: "https://api.example.com", operationCount: 3, readOperationCount: 2, mutatingOperationCount: 1, authSchemes: ["bearer"], warnings: [], checksumSha256: "a".repeat(64), status: "imported", sourceName: null, createdAt: "2026-11-12T08:00:00.000Z" };
const TABLES = Array.from({ length: 203 }, (_, i) => `t${i}`);
const PREVIEW_SOURCE = { id: DATA_SOURCE_ID, name: "erp", host: "gj.internal" };
const PROBLEMS = [{ field: "host", message: "The host is empty." }, { field: "host", message: "Two." }, { field: null, message: "GraphJin did not accept this change.", detail: "x" }, { field: "general", message: "Other." }];
const REGISTER_ARGS = { action: "register_source", name: " erp ", kind: ["database"], type: "mysql", port: 3306, dbname: "erp", user: "reader", secretRef: "erp-password", blockedTables: ["payroll", "audit"], adminTables: [], bucket: "unused", intent: "Add the ERP." };
const CHECK_ARGS = { type: "postgres", host: " db.internal ", port: 5432, dbname: "erp", user: "reader", secretRef: "erp-password" };

CALLS.push(
  admin("source graph", SOURCE_CONFIG, "source_config_manager_describe_source_graph", {}, { replies: { describeSourceGraph: { value: { reachable: true, sourceName: "erp", host: "gj.internal", databases: [{ id: "d1", name: "erp", summary: "postgres" }], capabilities: [], namespaces: [] } } } }),
  admin("source graph no arguments", SOURCE_CONFIG, "source_config_manager_describe_source_graph", undefined, { replies: { describeSourceGraph: { value: { reachable: false, error: "no data source configured" } } } }),
  admin("source graph fails", SOURCE_CONFIG, "source_config_manager_describe_source_graph", {}, { replies: { describeSourceGraph: { throws: "broker /v1/source-graph/describe failed: 500" } } }),
  admin("source config", SOURCE_CONFIG, "source_config_manager_describe_source_config", { dataSourceId: DATA_SOURCE_ID }, { replies: { describeSourceConfig: { value: { source: { id: DATA_SOURCE_ID, name: "erp", host: "gj.internal" }, catalogRevision: "r1", config: { sources: [] }, summary: { sources: [], roles: [] } } } } }),
  admin("source config default", SOURCE_CONFIG, "source_config_manager_describe_source_config", {}, { replies: { describeSourceConfig: { value: { denied: true, error: "GraphJin config tools are admin-only." } } } }),
  admin("source config bad id", SOURCE_CONFIG, "source_config_manager_describe_source_config", { dataSourceId: "erp" }),
  admin("secret names", SOURCE_CONFIG, "source_config_manager_list_source_secret_names", { extra: 1 }, { replies: { listSourceSecretNames: { value: { names: [{ name: "erp-password", description: null, updatedAt: "2026-11-12T08:00:00.000Z" }] } } } }),
  admin("source form empty", SOURCE_CONFIG, "source_config_manager_present_source_form", {}),
  admin("source form prefill", SOURCE_CONFIG, "source_config_manager_present_source_form", { name: " warehouse ", kind: "database", type: "mysql", host: "db.internal", port: 3306, dbname: "erp", user: "reader", secretRef: "erp-password", blockedTables: "admin_user, oauth_token", adminTables: "payroll", extra: "dropped" }),
  admin("source form files", SOURCE_CONFIG, "source_config_manager_present_source_form", { name: "docs", kind: "file", backend: "s3", bucket: "b", prefix: "p/", region: "eu-west-1", endpoint: "", publicBaseUrl: "https://cdn.example.com", presignTtl: "1h" }),
  admin("source form faults", SOURCE_CONFIG, "source_config_manager_present_source_form", { name: "n".repeat(65), kind: "sheet", type: "oracle", port: 0, host: 5, presignTtl: "x".repeat(17) }),
  admin("source form events fail", SOURCE_CONFIG, "source_config_manager_present_source_form", { name: "erp" }, { eventsFail: true }),
  admin("source check passes", SOURCE_CONFIG, "source_config_manager_check_source", CHECK_ARGS, { replies: { checkSource: { value: { check: { ok: true, serverVersion: "16.4", findings: [{ step: "login", status: "pass", message: "ok" }], tables: TABLES, moreTables: 4 } } } } }),
  admin("source check fails", SOURCE_CONFIG, "source_config_manager_check_source", { type: "mariadb", host: "db", dbname: "erp", user: "reader", secretRef: "erp-password" }, { replies: { checkSource: { value: { check: { ok: false, serverVersion: null, findings: [{ step: "port", status: "fail", message: "The port does not answer.", sql: "GRANT SELECT" }], tables: [], moreTables: 0 } } } } }),
  admin("source check denied", SOURCE_CONFIG, "source_config_manager_check_source", CHECK_ARGS, { replies: { checkSource: { value: { denied: true, error: "GraphJin config tools are admin-only." } } } }),
  admin("source check faults", SOURCE_CONFIG, "source_config_manager_check_source", { type: "oracle", host: " ", port: 70000, dbname: "erp", user: "u".repeat(129), secretRef: "pass word" }),
  admin("source check missing fields", SOURCE_CONFIG, "source_config_manager_check_source", {}),
  admin("pack setup", SOURCE_CONFIG, "source_config_manager_present_pack_setup", { packId: " magento " }),
  admin("pack setup bad id", SOURCE_CONFIG, "source_config_manager_present_pack_setup", { packId: "Magento!" }),
  admin("pack setup events fail", SOURCE_CONFIG, "source_config_manager_present_pack_setup", { packId: "shopify" }, { eventsFail: true }),
  admin("openapi import", SOURCE_CONFIG, "source_config_manager_import_openapi_spec", { url: "https://api.example.com/openapi.yaml", baseUrl: "https://api.example.com/v2" }, { replies: { importOpenApiSpec: { value: { asset: SPEC_ASSET } } } }),
  admin("openapi import fails", SOURCE_CONFIG, "source_config_manager_import_openapi_spec", { url: "https://api.example.com/openapi.yaml" }, { replies: { importOpenApiSpec: { value: { error: "OpenAPI URL must use HTTPS" } } } }),
  admin("openapi import bad url", SOURCE_CONFIG, "source_config_manager_import_openapi_spec", { url: "not a url", baseUrl: 7 }),
  admin("openapi list", SOURCE_CONFIG, "source_config_manager_list_openapi_specs", { limit: 5 }, { replies: { listOpenApiSpecs: { value: { assets: [SPEC_ASSET] } } } }),
  admin("openapi list default", SOURCE_CONFIG, "source_config_manager_list_openapi_specs", {}, { replies: { listOpenApiSpecs: { value: { assets: [] } } } }),
  admin("openapi list bad limit", SOURCE_CONFIG, "source_config_manager_list_openapi_specs", { limit: 0 }),
  admin("config change denied", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "add_role", name: " analyst ", match: "user.role == 'analyst'", intent: " Analysts read. " }, { replies: { previewSourceConfigChange: { value: { denied: true, error: "GraphJin config tools are admin-only." } } } }),
  admin("config change error", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "set_source_access", source: "erp", read: "admin", dataSourceId: DATA_SOURCE_ID, intent: "Admins only." }, { replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, error: "GraphJin did not return catalog_revision" } } } }),
  admin("config change register problems", SOURCE_CONFIG, "source_config_manager_request_source_config_change", REGISTER_ARGS, { replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, problems: PROBLEMS, error: "The host is empty. Two. GraphJin did not accept this change." } } } }),
  admin("config change register problems events fail", SOURCE_CONFIG, "source_config_manager_request_source_config_change", REGISTER_ARGS, { eventsFail: true, replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, problems: PROBLEMS, error: "x" } } } }),
  admin("config change register long prefix", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "register_source", name: "docs", kind: "file", backend: ["s3"], bucket: "b", prefix: "p".repeat(300), intent: "Docs." }, { replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, problems: PROBLEMS, error: "x" } } } }),
  admin("config change tables problems", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "set_source_tables", source: "erp", blockedTables: [" payroll "], openTables: [], intent: "Hide payroll." }, { replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, problems: [{ field: "blockedTables", message: "No table." }], error: "No table." } } } }),
  admin("config change invalid preview", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "set_source_capabilities", source: "shop", apiWrite: true, intent: "Writes." }, { replies: { previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, preview: { valid: false, patchHash: "h", errors: ["bad"] } } } } }),
  admin("config change empty reply", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "add_role", name: "a", match: "b", intent: "x" }, { replies: { previewSourceConfigChange: { value: {} } } }),
  admin("config change ask", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "register_source", name: "orders", kind: ["api"], specAssetId: SPEC_ASSET.id, intent: "Orders API." }, { replies: { ...ADMIN_ASK.replies, previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, specAsset: SPEC_ASSET, preview: { valid: true, patchHash: "h", catalogRevision: "r1", expiresAt: null, scope: "managed_openapi", reloadMode: "staged_reload", changes: { action: "register_source" }, findings: [], errors: null } } } } }),
  admin("config change allow", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "enable_api_writes", source: "shop", spec: "orders", operation: "createOrder", allowedRoles: ["admin"], exposeAs: " create_order ", write: "admin", dataSourceId: DATA_SOURCE_ID, intent: "Orders." }, { replies: { ...ADMIN_ALLOW.replies, previewSourceConfigChange: { value: { preview: { valid: true, patchHash: "h", fileCheck: { checked: false, reason: "OPENNEKO_GRAPHJIN_CONFIG is not set" } } } } } }),
  admin("config change denied by rule", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "expose_api_operation", source: "shop", spec: "orders", operation: "op", exposeMutation: false, intent: "x" }, { replies: { evaluateActionPolicy: { value: { decision: "deny", reason: "no config from chat" } }, previewSourceConfigChange: { value: { source: PREVIEW_SOURCE, preview: { valid: true } } } } }),
  admin("config change preview fails", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "add_role", name: "a", match: "b", intent: "x" }, { replies: { previewSourceConfigChange: { throws: "broker /v1/source-config/preview failed: 500" } } }),
  admin("config change faults", SOURCE_CONFIG, "source_config_manager_request_source_config_change", { action: "drop", endpoint: "not a url", port: 0, secretRef: "pass word", kind: ["sheet"], allowedRoles: ["root"], localFiles: { sourceName: "d", ready: false, files: [], totalSize: 0 } }),
);

type ListCase = { name: string; servers: string[]; memoryReadOnly?: boolean; policy?: string; graphjinTools?: unknown[]; listThrows?: string; cardSchema?: string; workflowRunId?: string; packActions?: PackActionDescriptor[] };

const LISTS: ListCase[] = [
  { name: "memory, guides and files", servers: ["neko_memory", "neko_guides", "neko_files"] },
  { name: "read-only memory", servers: ["neko_memory"], memoryReadOnly: true },
  { name: "graphjin between memory and files", servers: ["neko_memory", "neko_graphjin", "neko_files"] },
  { name: "graphjin direct-governed", servers: ["neko_graphjin"], policy: "direct-governed" },
  { name: "graphjin list fails", servers: ["neko_memory", "neko_graphjin"], listThrows: "GraphJin MCP tools/list failed: 503" },
  { name: "graphjin lists nothing", servers: ["neko_graphjin"], graphjinTools: [] },
  { name: "interaction, cards and skills", servers: ["neko_interaction", "neko_ui", "neko_skills"] },
  { name: "rich cards", servers: ["neko_ui", "neko_memory"], cardSchema: "rich" },
  { name: "workflow action, output and pack actions", servers: ["neko_action", "neko_workflow_output", "neko_pack_actions"], workflowRunId: WF_RUN, packActions: PACK_ACTIONS },
  { name: "workflow and rule builders", servers: ["neko_workflow_builder", "neko_rule_builder"] },
  { name: "admin tools", servers: ["neko_user_manager", "neko_channel_manager", "neko_data_source_manager", "neko_audit"] },
  { name: "source config tools", servers: ["neko_source_config_manager"] },
];

const METHODS = [
  "searchWorkMemoryByContext",
  "rememberWorkMemory",
  "updateWorkMemory",
  "searchSkillsForRun",
  "searchFilesForRun",
  "readFileForRun",
  "queryFileTablesForRun",
  "evaluateActionPolicy",
  "createActionRequest",
  "enqueueActionExecute",
  "waitForActionExecution",
  "emitWorkflowOutput",
  "saveWorkflowWithTrigger",
  "listWorkflowsWithTriggers",
  "deleteWorkflow",
  "upsertActionPolicyByName",
  "listActionPolicies",
  "listUsers",
  "listGroups",
  "listChannels",
  "listDataSources",
  "listAuditTrail",
  "describeSourceGraph",
  "listSourceSecretNames",
  "importOpenApiSpec",
  "listOpenApiSpecs",
  "describeSourceConfig",
  "checkSource",
  "previewSourceConfigChange",
];

function fakeControlPlane(
  reply: Reply | undefined,
  calls: unknown[],
  graphjin: { graphjinTools?: unknown[]; listThrows?: string; replies?: Record<string, Reply> } = {},
): BrokerControlPlane {
  const plane: Record<string, (input: unknown) => Promise<unknown>> = {};
  for (const method of [...METHODS, "callGraphjinTool"]) {
    plane[method] = async (input) => {
      calls.push({ method, input: JSON.parse(JSON.stringify(input)) });
      const own = graphjin.replies?.[method] ?? reply;
      if (own?.throws) throw new Error(own.throws);
      return own?.value;
    };
  }
  plane.listGraphjinTools = async (input) => {
    calls.push({ method: "listGraphjinTools", input });
    if (graphjin.listThrows) throw new Error(graphjin.listThrows);
    return graphjin.graphjinTools ?? GJ_TOOLS;
  };
  return plane as unknown as BrokerControlPlane;
}

type Broker = { url: string; events: unknown[]; fail: boolean; server: Server };

async function startBroker(): Promise<Broker> {
  const broker = { url: "", events: [] as unknown[], fail: false } as Broker;
  broker.server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      if (broker.fail) {
        res.writeHead(500).end();
        return;
      }
      broker.events.push(...(JSON.parse(body) as { events: unknown[] }).events);
      res.writeHead(204).end();
    });
  });
  await new Promise<void>((resolve) => broker.server.listen(0, "127.0.0.1", resolve));
  broker.url = `http://127.0.0.1:${(broker.server.address() as AddressInfo).port}`;
  return broker;
}

function readTree(root: string, prefix = ""): Record<string, string> {
  const out: Record<string, string> = {};
  for (const entry of readdirSync(join(root, prefix)).sort()) {
    const rel = prefix ? `${prefix}/${entry}` : entry;
    if (statSync(join(root, rel)).isDirectory()) Object.assign(out, readTree(root, rel));
    else out[rel] = readFileSync(join(root, rel), "utf8");
  }
  return out;
}

async function connect(
  servers: string[],
  memoryReadOnly: boolean,
  controlPlane: BrokerControlPlane,
  policy?: string,
  extra: { skillsRoot?: string; brokerUrl?: string; wantsCards?: boolean; cardSchema?: string; workflowRunId?: string; observationId?: string; packActions?: PackActionDescriptor[] } = {},
) {
  const server = await buildMultiplexedBridgeServer(servers, {
    runKind: "work",
    orgId: "11111111-1111-4111-8111-111111111111",
    threadId: "22222222-2222-4222-8222-222222222222",
    runId: "33333333-3333-4333-8333-333333333333",
    skillsRoot: extra.skillsRoot ?? "/tmp/skills",
    packActions: extra.packActions ?? [],
    ...(extra.workflowRunId ? { workflowRunId: extra.workflowRunId } : {}),
    ...(extra.observationId ? { triggeredByObservationId: extra.observationId } : {}),
    controlPlane,
    memoryReadOnly,
    ...(extra.brokerUrl ? { brokerUrl: extra.brokerUrl, brokerToken: "broker-token" } : {}),
    ...(extra.wantsCards ? { wantsCards: true } : {}),
    ...(extra.cardSchema ? { cardSchema: extra.cardSchema } : {}),
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
      const client = await connect(list.servers, list.memoryReadOnly ?? false, fakeControlPlane(undefined, [], list), list.policy, { cardSchema: list.cardSchema, workflowRunId: list.workflowRunId, packActions: list.packActions });
      lists.push({ ...list, tools: (await client.listTools()).tools });
      await client.close();
    }
    const calls = [];
    const broker = await startBroker();
    for (const call of CALLS) {
      const recorded: unknown[] = [];
      const skillsRoot = mkdtempSync(join(tmpdir(), "neko-skills-"));
      broker.events = [];
      broker.fail = call.eventsFail ?? false;
      const client = await connect(call.servers, call.memoryReadOnly ?? false, fakeControlPlane(call.reply, recorded, call), call.policy, {
        skillsRoot,
        brokerUrl: broker.url,
        wantsCards: call.wantsCards,
        cardSchema: call.cardSchema,
        workflowRunId: call.workflowRunId,
        observationId: call.observationId,
        packActions: call.packActions,
      });
      let outcome: Record<string, unknown>;
      try {
        if (call.listFirst) await client.listTools();
        outcome = { result: await client.callTool({ name: call.tool, ...(call.arguments ? { arguments: call.arguments } : {}) }) };
      } catch (err) {
        outcome = { error: err instanceof Error ? err.message : String(err) };
      }
      const files = readTree(skillsRoot);
      rmSync(skillsRoot, { recursive: true, force: true });
      calls.push(JSON.parse(JSON.stringify({ ...call, ...outcome, controlPlane: recorded, events: broker.events, files })
        .replaceAll(skillsRoot, "<skills>")
        .replace(/(answer|source-form|pack-setup)-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g, "$1-<id>")));
      await client.close();
    }
    broker.server.close();
    const fixture = `${JSON.stringify({ graphjinTools: GJ_TOOLS, lists, calls }, null, 1)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
