import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { createScrubber } from "../src/work/secret-scrubber";
import {
  registerWorkSemanticTraceSink,
  traceFileSearch,
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
  operation: "memory.search" | "files.search";
  request: { query: string; limit?: number };
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
      const trace = c.operation === "memory.search" ? traceMemorySearch : traceFileSearch;
      await trace({ binding, request: c.request, execute }).catch(() => undefined);
      await trace({ binding, request: c.request, execute }).catch(() => undefined);
      unregister();
      out.push({ ...c, traced: got });
    }
    const fixture = `${JSON.stringify(out, null, 1)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
