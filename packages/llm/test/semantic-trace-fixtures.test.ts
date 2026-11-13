import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { createScrubber } from "../src/work/secret-scrubber";
import {
  registerWorkSemanticTraceSink,
  traceActionPolicy,
  traceFileSearch,
  traceGraphjinToolCall,
  traceGraphjinToolsList,
  traceMemorySearch,
  type WorkSemanticStepContent,
  type WorkSemanticTraceEvent,
} from "../src/work/semantic-trace";

// The Go trace (apps/openneko/internal/webfront/turntrace.go) must give the
// same events and step content. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite
// the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/turntrace.json");

type TraceCase = {
  name: string;
  operation: "memory.search" | "files.search" | "graphjin.tools_list" | "graphjin.call" | "action.policy";
  request: {
    query?: string;
    limit?: number;
    name?: string;
    arguments?: Record<string, unknown>;
    scope?: "internal" | "external";
    kind?: string;
    target?: string | null;
    riskLevel?: string | null;
  };
  reply: { value?: unknown; throws?: string };
  secrets?: string[];
};

const SECRET = "sk-live-0123456789abcdef";

const memory = (id: string, text: string, extra: Record<string, unknown> = {}) => ({
  source: "saved_memory",
  memory: { id, orgId: "org-1", kind: "business_rule", scope: "global", scopeId: null, text, userId: null, ...extra },
  score: 0.1,
});

const bigMatches = Array.from({ length: 40 }, (_, i) => ({
  path: `uploads/report-${i}.pdf`,
  score: 1 / (i + 3),
  passages: [{ location: `page ${i + 1}`, snippet: `${"net revenue ".repeat(40)}${i}` }],
}));

const CASES: TraceCase[] = [
  {
    name: "memory search with content",
    operation: "memory.search",
    request: { query: "revenue", limit: 3 },
    reply: {
      value: [
        memory("44444444-4444-4444-8444-444444444444", `Revenue is net. Key ${SECRET}.`, { Zeta: 1, alpha: [3, 1e-7, 123456789012] }),
        memory("55555555-5555-4555-8555-555555555555", "Café 漢字 rule", { userId: "user-1", scope: "thread", kind: "" }),
        { source: "saved_memory", memory: { text: "no id" }, score: 0 },
        { other: true },
      ],
    },
    secrets: [SECRET, "short"],
  },
  {
    name: "memory search fails",
    operation: "memory.search",
    request: { query: "revenue", limit: 5 },
    reply: { throws: `embedding service is off: ${SECRET}` },
    secrets: [SECRET],
  },
  {
    name: "memory search without content",
    operation: "memory.search",
    request: { query: "revenue", limit: 5 },
    reply: { value: [] },
  },
  {
    name: "files search with content",
    operation: "files.search",
    request: { query: "net revenue" },
    reply: {
      value: {
        matches: [
          { path: "uploads/q3.xlsx", score: 2.5, passages: [{ location: "sheet Sales", snippet: "net revenue" }] },
          { score: 1 },
          { path: "uploads/notes.md", score: 1.25 },
        ],
      },
    },
    secrets: [SECRET],
  },
  {
    name: "files search truncated",
    operation: "files.search",
    request: { query: "net revenue", limit: 20 },
    reply: { value: { matches: bigMatches } },
    secrets: [],
  },
  {
    name: "files search fails",
    operation: "files.search",
    request: { query: "net revenue" },
    reply: { throws: "File search is off: OPENNEKO_SERVE_URL is not set." },
  },
  {
    name: "graphjin tools list",
    operation: "graphjin.tools_list",
    request: {},
    reply: {
      value: [
        { name: "execute_graphql", description: "Run.", inputSchema: { type: "object", required: ["query"], properties: { query: { type: "string" } } } },
        { name: "graphql_help" },
        { name: "", inputSchema: { type: "object" } },
        { description: "no name" },
        "not a tool",
      ],
    },
    secrets: [SECRET],
  },
  {
    name: "graphjin tools list fails",
    operation: "graphjin.tools_list",
    request: {},
    reply: { throws: `GraphJin MCP tools/list failed: 401 bad token ${SECRET}` },
    secrets: [SECRET],
  },
  {
    name: "graphjin execute with rows",
    operation: "graphjin.call",
    request: { name: "execute_graphql", arguments: { query: "  # top orders\nquery Top { orders(limit: 2) { id note } }", variables: { limit: 2, key: SECRET } } },
    reply: {
      value: {
        content: [{ type: "text", text: JSON.stringify({ data: { orders: [{ id: 1, note: `uses ${SECRET}` }, { id: 2, note: null }], total: { count: 2 }, empty: null } }) }],
      },
    },
    secrets: [SECRET],
  },
  {
    name: "graphjin execute with errors",
    operation: "graphjin.call",
    request: { name: "execute_graphql", arguments: { query: "mutation { delete_orders { id } }" } },
    reply: { value: { content: [{ type: "text", text: JSON.stringify({ data: null, errors: [{ message: "source is read_only" }] }) }] } },
    secrets: [],
  },
  {
    name: "graphjin execute no query",
    operation: "graphjin.call",
    request: { name: "execute_graphql", arguments: {} },
    reply: { value: { isError: true, content: [{ type: "text", text: "query is required" }] } },
  },
  {
    name: "graphjin catalog",
    operation: "graphjin.call",
    request: { name: "query_catalog", arguments: { ids: ["t1", "t2"], kind: "table" } },
    reply: { value: { content: [{ type: "text", text: "[{\"id\":\"t1\"},{\"id\":\"t2\"}]" }, { type: "text", text: "plain words" }, { type: "image", data: "AAAA", mimeType: "image/png" }] } },
    secrets: [SECRET],
  },
  {
    name: "graphjin other tool fails",
    operation: "graphjin.call",
    request: { name: "validate_where_clause" },
    reply: { throws: "No data source is available to this run." },
    secrets: [],
  },
  {
    name: "policy allows",
    operation: "action.policy",
    request: { scope: "external", kind: "slack_post", target: "C123", riskLevel: "low" },
    reply: {
      value: {
        decision: "allow",
        policy: { id: "88888888-8888-4888-8888-888888888888", orgId: "org-1", name: "slack_auto", allowedTargets: { patterns: [SECRET] }, priority: 100, createdAt: "2026-11-13T00:00:00.000Z" },
        mode: "auto_approve",
      },
    },
    secrets: [SECRET],
  },
  {
    name: "policy needs approval without target",
    operation: "action.policy",
    request: { scope: "internal", kind: "user_admin", target: null, riskLevel: null },
    reply: { value: { decision: "needs_approval", policy: { id: "p-2", name: "user_management_default" }, mode: "approval_required", reason: "policy \"user_management_default\" mode=approval_required" } },
    secrets: [],
  },
  {
    name: "policy denies by name only",
    operation: "action.policy",
    request: { scope: "external", kind: "send_email", target: "", riskLevel: "high" },
    reply: { value: { decision: "deny", policy: { name: "no_email" }, mode: "never", reason: "policy \"no_email\" mode=never" } },
  },
  {
    name: "policy finds none",
    operation: "action.policy",
    request: { scope: "external", kind: "crm_update", target: "lead/7", riskLevel: null },
    reply: { value: { decision: "no_policy", reason: "no_matching_policy" } },
    secrets: [SECRET],
  },
  {
    name: "policy check fails",
    operation: "action.policy",
    request: { scope: "external", kind: "slack_post", target: "C123", riskLevel: null },
    reply: { throws: `policy store is down: ${SECRET}` },
    secrets: [SECRET],
  },
];

function stable(event: WorkSemanticTraceEvent) {
  const { timestamp: _t, durationMs: _d, ...rest } = event;
  return rest;
}

describe("semantic trace fixtures for the Go trace", () => {
  it("matches the fixture the Go test reads", async () => {
    const out = [];
    for (const c of CASES) {
      const runId = `run-${c.name}`;
      const got: Array<{ event: unknown; content?: WorkSemanticStepContent }> = [];
      const unregister = registerWorkSemanticTraceSink(
        runId,
        (event, content) => {
          got.push({ event: stable(event), ...(content ? { content } : {}) });
        },
        c.secrets ? { captureContent: { scrub: createScrubber(c.secrets) } } : {},
      );
      const binding = { runId, orgId: "org-1", kind: "work" as const };
      const execute = async () => {
        if (c.reply.throws) throw new Error(c.reply.throws);
        return c.reply.value;
      };
      const once = () => {
        switch (c.operation) {
          case "memory.search":
            return traceMemorySearch({ binding, request: c.request as { query: string }, execute });
          case "files.search":
            return traceFileSearch({ binding, request: c.request as { query: string }, execute });
          case "graphjin.tools_list":
            return traceGraphjinToolsList({ binding, execute });
          case "action.policy":
            return traceActionPolicy({
              binding,
              request: {
                scope: c.request.scope ?? "external",
                kind: c.request.kind ?? "",
                target: c.request.target ?? null,
                riskLevel: c.request.riskLevel ?? null,
              },
              execute: execute as () => Promise<{ decision: "allow" | "needs_approval" | "deny" | "no_policy" }>,
            });
          case "graphjin.call":
            return traceGraphjinToolCall({
              binding,
              toolName: c.request.name ?? "",
              ...(c.request.arguments !== undefined ? { arguments: c.request.arguments } : {}),
              execute,
            });
        }
      };
      await once().catch(() => undefined);
      await once().catch(() => undefined);
      unregister();
      out.push({ ...c, traced: got });
    }
    const fixture = `${JSON.stringify(out, null, 1)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
