// Shared prompt sections used by every Neko agent prompt builder
// (work chat, metric agent, and — soon — workflow agents). Anything that
// describes how to talk to GraphJin, what memories to apply, or the
// canonical data-access rules belongs here so the wording can't drift
// between agents.

import { knowledgePackPaths, type KnowledgePackContents } from "../knowledge-pack";
import type { AgentWorkspace } from "../agent-backend";

export const GRAPHJIN_DATE_RULE = `- For GraphJin date/range filters, do not put multiple operators under
  the same column object. Use
  \`where: { and: [{ orderdate: { gte: "2024-06-30" } },
                   { orderdate: { lte: "2025-06-29" } }] }\`
  rather than \`where: { orderdate: { gte: "...", lte: "..." } }\`.`;

// Anti-fanout rule we keep separately so it can be cited in the
// metric-agent's HARD CONSTRAINTS *and* in the chat agent's data-access
// rules without copy-paste drift. Born from the CEO #1 fact-check.
export const GRAPHJIN_FANOUT_RULE = `- A nested GraphJin response is flattened — one row per child. Summing
  the parent column from that flattened payload double-counts by N
  children per parent. To get a correct parent-side total either (a)
  sum at the parent root only with no nested children, (b) use
  \`distinct: [parent_id]\` to deduplicate first, or (c) split into two
  queries — one parent-side aggregate and one child-side aggregate.`;

export type MemorySaveMode = "tool" | "fence" | "none";

export type MemorySectionOptions = {
  /** True when the agent has the `mcp__neko_memory__search` MCP tool. */
  searchTool: boolean;
  /**
   * How the agent can persist new memories:
   * - "tool": call `mcp__neko_memory__save`
   * - "fence": emit a ```neko_memory fenced block (parsed post-run)
   * - "none": agent does not write memories (operator does it explicitly)
   */
  saveMode: MemorySaveMode;
  /** Prefetched memory list (string). Undefined / empty → "no memories" placeholder. */
  memoryContext: string | undefined;
};

export function buildMemorySection(opts: MemorySectionOptions): string {
  const { searchTool, saveMode, memoryContext } = opts;

  const loaded = memoryContext?.trim()
    ? memoryContext.trim()
    : "No memories are currently saved for this workspace.";

  const application = `Apply these memories when relevant. They are
operator-validated rules and facts and **take precedence over default
behavior described elsewhere in this prompt** — if a memory contradicts
a default, the memory wins. When you act on a memory, briefly cite it
in your reasoning so the operator can verify (e.g.
"applied memory: don't sum from a flattened nested response"). Don't
silently ignore a relevant memory.`;

  const usageBlocks: string[] = [];

  if (searchTool) {
    usageBlocks.push(`To find related memories beyond the ones loaded above: call
\`mcp__neko_memory__search\` with a short natural-language query. Do
this whenever the user's request mentions a domain, metric, or rule
that isn't already covered by the preloaded list.`);
  }

  if (saveMode === "tool") {
    usageBlocks.push(`To save a new memory: call \`mcp__neko_memory__save\` with the
exact rule the operator stated. Use \`global\` scope unless they say
it's only for this thread.`);
  } else if (saveMode === "fence") {
    usageBlocks.push(`To save a new memory mid-conversation, emit a fenced block:

\`\`\`neko_memory
[{ "save": { "text": "the exact rule the operator stated",
             "scope": "global", "kind": "business_rule",
             "pinned": true } }]
\`\`\`

The runtime parses the fence and persists each entry. Multiple
\`{ "save": ... }\` items in the array are allowed. The block is
removed from the user-visible output. Only emit this when the operator
explicitly says to remember/save something — never speculatively.`);
  }

  const usage = usageBlocks.length > 0 ? `\n\n${usageBlocks.join("\n\n")}` : "";

  return `<long_term_memory>
${loaded}

${application}${usage}
</long_term_memory>`;
}

export type DataAccessOptions = {
  shellTool: string;
  workspace: AgentWorkspace;
  knowledge: KnowledgePackContents;
  // 'syntax': inline only the DSL reference, point at the other knowledge
  // files for the agent to read on demand. Best for interactive paths.
  // 'all': inline tables + namespaces + insights + syntax. Best for
  // one-shot agents (metric, single-card) that can't iterate.
  inlineKnowledge: "syntax" | "all";
};

export function buildDataAccessSection(opts: DataAccessOptions): string {
  const { shellTool, workspace, knowledge, inlineKnowledge } = opts;
  const paths = knowledgePackPaths(workspace.knowledgeRoot);

  // Agentic deployments (GraphJin sources mode, GJ4 actor tokens) layer
  // knowledge differently: a slim role-aware bootstrap is inlined and
  // everything deeper is discovered ON DEMAND through gj_catalog queries
  // that run under the caller's own token.
  if (knowledge.mode === "agentic") {
    return buildAgenticDataAccessSection(opts, paths);
  }

  const knowledgeBlock =
    inlineKnowledge === "all"
      ? `================================================================================
Tables — every table in the database (name, schema, column_count):
================================================================================

${knowledge.tables}

================================================================================
Namespaces — multi-database routing context:
================================================================================

${knowledge.namespaces}

================================================================================
Insights — hub tables, hot relationships, relationship paths, query templates, data-quality flags:
================================================================================

${knowledge.insights}

================================================================================
GraphJin DSL reference (syntax.json) — operators, aggregations, pagination, expression aggregates, common mistakes. Authoritative.
================================================================================

${knowledge.syntax}`
      : `================================================================================
GraphJin DSL reference (syntax.json) — operators, aggregations,
pagination, expression aggregates, common mistakes. Authoritative.
================================================================================

${knowledge.syntax}`;

  const fileGuidance =
    inlineKnowledge === "syntax"
      ? `Supplementary knowledge lives on disk (read with your \`${shellTool}\`
tool when targeted lookup helps — but the DSL below is authoritative,
don't re-derive it from these files):

- ${paths.files.index} (start here — index of the rest)
- ${paths.files.tables} (every table, schema, column count)
- ${paths.files.namespaces} (multi-DB namespace routing, if any)
- ${paths.files.insights} (hub tables, hot relationships, relationship paths, query templates)

`
      : "";

  return `<data_access>
The configured GraphJin database is the authoritative source for any
operational question (revenue, customers, orders, inventory, employees,
sales, products, etc.). When the user attaches a file or explicitly
references uploaded data, read the file and use it — it's the source of
truth for that turn. Otherwise default to the database.

The GraphJin DSL reference is inlined below in full — operators,
aggregations, pagination, expression aggregates, and the common
mistakes that produce "OpQuery: expecting an aliased field name" or
"table not found: <name>_sum" errors. Do NOT \`cat\` it from disk;
shell-tool output is capped and you'll lose the aggregation examples
that sit past the cap. Just read it from this prompt.

${fileGuidance}Do NOT call \`graphjin cli list_tables\` / \`describe_table\` /
\`get_query_syntax\` / \`get_schema_insights\` / \`get_discovery_schema\` —
those broad discovery dumps duplicate work that's already prefetched.

Run queries via the \`${shellTool}\` tool:

  graphjin cli execute_graphql --args '{"query":"<your read-only graphql>"}'

If a response contains an \`errors\` array, run:

  graphjin cli fix_query_error --args '{"query":"<failing>","error":"<msg>"}'

to get a corrected query, then run execute_graphql again.

These targeted read-only tools are also available when they help:

  graphjin cli get_table_sample --args '{"table":"<name>"}'
    Call before writing a filter on a string or enum column. The
    response includes real distinct values with row counts (e.g.
    city: "Toronto" 1037, "New York" 664), available aggregations,
    foreign keys, and analytics-mode rules. Without it you're
    guessing literals — "Toronto" vs "TORONTO", "Cell phones" vs
    "Cellphones" — and a wrong guess returns zero rows silently.
  graphjin cli find_path --args '{"from_table":"<table>","to_table":"<table>"}'
  graphjin cli explore_relationships --args '{"table":"<name>"}'

For metric / time-series / top-N shapes, lift the template from the
\`patterns\` block in the inlined syntax below (\`metric_by_dimension\`,
\`time_series\`, \`top_n\`) and substitute real names into the
placeholders in \`right_example\`. Each pattern's \`rule\` field tells
you where to root the query — getting that wrong (e.g. rooting at the
fact table and trying to bucket with \`distinct\`) is the most common
cause of compile errors on aggregating queries.

Talk to GraphJin only through \`${shellTool}\` running \`graphjin cli\`.
\`execute_code\`, Python, raw HTTP, or any other path bypasses the tool
gate that blocks mutations and subscriptions, and produces results the
rest of the system can't trace. Mutations and subscriptions are blocked
at the tool gate regardless.

For date/range filters: ${GRAPHJIN_DATE_RULE.replace(/^- /, "")}

${GRAPHJIN_FANOUT_RULE.replace(/^- /, "")}

Never invent or interpolate. If a query returned no rows, the answer
is "no data", not a guess.

${knowledgeBlock}
</data_access>`;
}

const TABLE_DIGEST_MAX_CHARS = 4_000;
const INSIGHTS_DIGEST_MAX_CHARS = 6_000;

/** Hub tables with ready query templates and join paths, from the agentic
 *  pack's insights.json — the part of the legacy pack that made first
 *  answers fast. Compact and hard-capped: NEVER inline raw pack JSON
 *  (a 26KB inline reproducibly hung the model stream). */
export function compactInsightsDigest(raw: string): string {
  let hubs: Array<{
    name?: string;
    summary?: string;
    examples?: unknown[];
    join_paths?: unknown[];
  }>;
  try {
    const parsed = JSON.parse(raw) as { hub_tables?: typeof hubs };
    hubs = Array.isArray(parsed.hub_tables) ? parsed.hub_tables : [];
  } catch {
    return "";
  }
  if (hubs.length === 0) return "";
  let out = "";
  for (const hub of hubs) {
    let block = `## ${hub.name ?? "?"}${hub.summary ? ` — ${String(hub.summary).slice(0, 110)}` : ""}\n`;
    for (const path of (hub.join_paths ?? []).slice(0, 6)) {
      block += `  join: ${String(path).slice(0, 140)}\n`;
    }
    for (const ex of (hub.examples ?? []).slice(0, 2)) {
      const q =
        typeof ex === "string"
          ? ex
          : ((ex as { query?: string }).query ?? JSON.stringify(ex));
      block += `  template: ${q.replace(/\s+/g, " ").slice(0, 300)}\n`;
    }
    if (out.length + block.length > INSIGHTS_DIGEST_MAX_CHARS) break;
    out += block;
  }
  return out.trimEnd();
}

const HELP_INDEX_MAX_CHARS = 2_000;

/** One line per help card from the agentic pack's insights file. The raw
 *  file also carries hub_tables (rendered separately by
 *  compactInsightsDigest) — inlining it verbatim duplicates that and
 *  reinflates the prompt the digests exist to shrink. */
export function compactHelpCardIndex(raw: string): string {
  let cards: Array<{ id?: string; summary?: string }>;
  try {
    const parsed = JSON.parse(raw) as { help_cards?: typeof cards };
    cards = Array.isArray(parsed.help_cards) ? parsed.help_cards : [];
  } catch {
    return "";
  }
  let out = "";
  for (const card of cards) {
    if (!card.id) continue;
    const line = `- ${card.id}${card.summary ? ` — ${String(card.summary).slice(0, 90)}` : ""}\n`;
    if (out.length + line.length > HELP_INDEX_MAX_CHARS) break;
    out += line;
  }
  return out.trimEnd();
}

/** One short line per table from the pack's tables file (legacy or agentic
 *  shape), hard-capped — a prompt block, not a schema dump. */
export function compactTableDigest(raw: string): string {
  let lines: string[];
  try {
    const parsed = JSON.parse(raw) as {
      tables?: Array<{
        name?: string;
        schema?: string;
        database?: string;
        column_count?: number;
        summary?: string;
        id?: string;
      }>;
    };
    const tables = Array.isArray(parsed.tables) ? parsed.tables : [];
    if (tables.length === 0) throw new Error("no tables array");
    lines = tables.map((t) => {
      const name = [t.schema, t.name].filter(Boolean).join(".") || t.id || "?";
      const extra = t.summary
        ? ` — ${String(t.summary).slice(0, 60)}`
        : t.column_count != null
          ? ` (${t.column_count} cols)`
          : "";
      return `- ${name}${extra}`;
    });
  } catch {
    lines = raw.split("\n").filter((l) => l.trim());
  }
  let out = "";
  let dropped = 0;
  for (const line of lines) {
    if (out.length + line.length + 1 > TABLE_DIGEST_MAX_CHARS) {
      dropped = lines.length - out.split("\n").filter(Boolean).length;
      break;
    }
    out += line + "\n";
  }
  if (dropped > 0) {
    out += `… ${dropped} more — list the rest via gj_catalog (kind: "table").\n`;
  }
  return out.trimEnd();
}

function buildAgenticDataAccessSection(
  opts: DataAccessOptions,
  paths: ReturnType<typeof knowledgePackPaths>,
): string {
  const { shellTool, knowledge } = opts;

  // Inline a COMPACT table digest in agentic mode: pointing the model at a
  // file costs every question several discovery calls before its first real
  // query (measured ~3x slower to first answer). But the raw pack file is
  // pretty-printed JSON (10s of KB) and inlining it verbatim broke runs —
  // compress to one short line per table and hard-cap the block.
  const tablesBlock = `================================================================================
Tables visible to your role (deeper detail via gj_catalog on demand):
================================================================================

${compactTableDigest(knowledge.tables)}

${(() => {
  const insights = compactInsightsDigest(knowledge.insights);
  return insights
    ? `================================================================================
Hub tables — join paths and ready query templates (adapt, don't rediscover):
A join path "s1.child.col -> s2.parent.col" means child rows reference parent;
traverse it by nesting in one query: { child(limit: 20) { col parent { ... } } }.
================================================================================

${insights}

`
    : "";
})()}`;

  return `<data_access>
The configured GraphJin database is the authoritative source for any
operational question (revenue, customers, orders, inventory, employees,
sales, products, etc.). When the user attaches a file or explicitly
references uploaded data, read the file and use it — it's the source of
truth for that turn. Otherwise default to the database.

This deployment runs GraphJin in agentic (sources) mode: schema
knowledge is DISCOVERED ON DEMAND through the \`gj_catalog\` root, and
every query — including catalog queries — runs under YOUR access token,
so you only ever see what your role allows. A slim bootstrap is
provided; do not assume it is the whole schema.

Discovery pattern (all via the \`${shellTool}\` tool):

  graphjin cli execute_graphql --args '{"query":"query { gj_catalog(search: \\"<what you need>\\", limit: 10) { id kind name summary } }"}'
  graphjin cli execute_graphql --args '{"query":"query { gj_catalog(id: \\"table:<db>:<schema>.<table>\\") { id name summary details_json examples_json edges_json } }"}'
  graphjin cli execute_graphql --args '{"query":"query { gj_catalog(where: { kind: { eq: \\"column\\" } }, search: \\"<table>\\", limit: 30) { id name summary } }"}'

Catalog row kinds: help, database, table, column, relationship,
function, capability. \`gj_catalog(id: "...")\` returns one detailed
card — details_json carries columns/types/keys, examples_json carries
ready-to-adapt queries, edges_json carries join paths. When unsure
where to look, pull \`gj_catalog(id: "help:discovery")\`.

Discover before you query: pull the table card (and column rows for
filter literals) before writing a non-trivial query — guessing column
names or string literals returns zero rows silently. For join planning,
\`find_path\` and \`explore_relationships\` remain available:

  graphjin cli find_path --args '{"from_table":"<table>","to_table":"<table>"}'
  graphjin cli explore_relationships --args '{"table":"<name>"}'

Run data queries the same way:

  graphjin cli execute_graphql --args '{"query":"<your read-only graphql>"}'

If a response contains an \`errors\` array, check
\`errors[].extensions.graphjin_repair\` first (it often contains the
corrected query), else run:

  graphjin cli fix_query_error --args '{"query":"<failing>","error":"<msg>"}'

Talk to GraphJin only through \`${shellTool}\` running \`graphjin cli\`.
\`execute_code\`, Python, raw HTTP, or any other path bypasses the tool
gate that blocks mutations and subscriptions, and produces results the
rest of the system can't trace. Mutations and subscriptions are blocked
at the tool gate regardless.

For date/range filters: ${GRAPHJIN_DATE_RULE.replace(/^- /, "")}

${GRAPHJIN_FANOUT_RULE.replace(/^- /, "")}

Never invent or interpolate. If a query returned no rows, the answer
is "no data", not a guess.

${tablesBlock}================================================================================
Help-card index — what the catalog can teach you (pull any card's full
guidance on demand with gj_catalog(id: "help:<topic>")):
================================================================================

${compactHelpCardIndex(knowledge.insights)}

================================================================================
Query-DSL essentials — filters, query shape, and the aggregate patterns
(distinct + sum_<col> replaces row pagination). Pull other help cards
for mutations, fragments, errors:
================================================================================

${knowledge.syntax}
</data_access>`;
}
