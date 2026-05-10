/**
 * Business profile agent — backend-agnostic.
 *
 * Per-org backend (Hermes or Claude Agent) is resolved from
 * llm_provider_config (scope='agent') by `resolveAgentBackend`. Same
 * mechanism the metric agent uses, so the two stay in lockstep.
 *
 * The agent reads inline GraphJin discovery (tables / insights / syntax),
 * issues GraphQL via `graphjin cli execute_graphql` over Bash, and emits
 * the business profile markdown directly as its final assistant message.
 *
 * Output contract: stdout MUST be the profile body — markdown starting
 * with `# {Company Name} — Business Profile`. No code fences, no preamble.
 * The caller (apps/worker/src/jobs/business-profile-build.ts) writes it
 * verbatim into customer_profile.business_profile.
 */

import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";

const DISCOVERY_RETRIES = 4;
const DISCOVERY_TIMEOUT_MS = 120_000; // generous for cold-start lazy generation

async function fetchDiscovery(url: string, label: string): Promise<string> {
  let lastErr: unknown;
  for (let attempt = 1; attempt <= DISCOVERY_RETRIES; attempt++) {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), DISCOVERY_TIMEOUT_MS);
    try {
      const res = await fetch(url, { signal: ctrl.signal });
      if (!res.ok) {
        throw new Error(`${res.status} ${res.statusText}`);
      }
      return await res.text();
    } catch (err) {
      lastErr = err;
      const msg = err instanceof Error ? err.message : String(err);
      console.warn(
        `[profiler] discovery ${label} attempt ${attempt}/${DISCOVERY_RETRIES} failed: ${msg}`,
      );
      if (attempt < DISCOVERY_RETRIES) {
        await new Promise((r) => setTimeout(r, 1000 * 2 ** (attempt - 1)));
      }
    } finally {
      clearTimeout(t);
    }
  }
  const msg = lastErr instanceof Error ? lastErr.message : String(lastErr);
  throw new Error(`discovery ${label} fetch failed after ${DISCOVERY_RETRIES} attempts: ${msg}`);
}

type Knowledge = { tables: string; insights: string; syntax: string };

function buildPrompt(args: {
  orgName: string;
  companyNote: string;
  k: Knowledge;
  shellTool: string;
}): string {
  const { orgName, companyNote, k, shellTool } = args;
  return `You build a short markdown business profile about a customer company by querying its database via GraphJin.

EXECUTION PATTERN:
1. Read the three knowledge sections below (Tables, Insights, Syntax) — together they are the authoritative DSL + table/column/FK index for this database. Don't run any schema-discovery commands; that information is already inline.
2. Skim Tables + Insights to identify what this business actually does (industry, offering, business model). Pick the handful of tables that matter.
3. Run a small set of GraphQL queries to gather facts: main business event (date range, recent volume + value), top categories / products / services, geography, who is served, who does the work.
4. Run queries via the \`${shellTool}\` tool:
     graphjin cli execute_graphql --args '{"query":"<your graphql>"}'
   (\`graphjin cli\` is already pointed at the running server. Use --args-file - and stdin if a query is large enough to be awkward to escape inline.)
5. If a response contains an "errors" array, use:
     graphjin cli fix_query_error --args '{"query":"<failing query>","error":"<error message>"}'
   to get a corrected query, then run execute_graphql again.
6. When you have enough facts, emit the final markdown body exactly per the OUTPUT FORMAT. No prose around it, no code fences.

DATA ACCESS — READ-ONLY:
The database is queried exclusively via \`graphjin cli\` run through the \`${shellTool}\` tool. GraphJin speaks GraphQL (not raw SQL). Mutations and subscriptions are forbidden and will be denied at the tool gate. DO NOT use \`execute_code\`, Python, raw HTTP requests, or any other tool to talk to GraphJin — only \`${shellTool}\` running \`graphjin cli\`.

- Every database read goes through \`graphjin cli execute_graphql\` via \`${shellTool}\`.
- DO NOT call \`graphjin cli list_tables\`, \`describe_table\`, \`get_query_syntax\`, etc. — that info is already inline below.
- Other useful subcommands: \`graphjin cli explain --args '{"query":"..."}'\` (compile-only, no execution); \`graphjin cli health\` (sanity check).
- Never invent data — every number in the profile must trace back to a \`graphjin cli execute_graphql\` response from this run.

QUERY CONSTRUCTION — let the database aggregate:
Prefer one bulk query with server-side aggregation (count, sum, avg) over multiple round-trips that pull rows back to the agent. Specific GraphJin capabilities to reach for (full details in the Reference below):

- Expression aggregates — sum(expr: {...}), ratio(expr: {...}) — USE THESE FIRST when a fact involves arithmetic across columns (e.g. SUM(price × qty)).
- Joined-column access via dot-notation: { col: "product.standardcost" } works across FKs up to 3 hops.
- order_by on an expression alias: server-side top-N by computed metric, no over-fetch.
- Global single-row aggregate: a top-level select whose fields are ALL aggregates collapses to one row, no distinct needed.

HARD CONSTRAINTS (violating any of these is a critical failure):
- Never hardcode calendar years; compute periods with relative arithmetic. If you need an anchor date, use a recent date from the data.
- Never use a bare limit without pagination. Use cursor-based pagination to process all rows, or use GraphQL aggregation with distinct to let the database aggregate.
- Watch the silent 20-row default limit on every query level (top AND nested) — set explicit limit or use distinct+aggregation.
- Never invent or interpolate. If a query returned no rows, the answer is "Not measured.", not a guess.

================================================================================
Tables — every table in the database (name, schema, column_count):
================================================================================

${k.tables}

================================================================================
Insights — hub tables, hot relationships, query templates, data-quality flags:
================================================================================

${k.insights}

================================================================================
Syntax — authoritative GraphJin DSL reference (operators, aggregations, pagination):
================================================================================

${k.syntax}

================================================================================
OUTPUT FORMAT — respond with EXACTLY this markdown body, no code fences, no prose around it:
================================================================================

# ${orgName} — Business Profile

## What they are
1–2 sentences: industry, offering, business model.

## Who they serve
Recipients in the business's own terms (customers, patients, members, accounts, …) with counts.

## Where they operate
Geographic / facility footprint with real names.

## Scale
- Date range covered, recent volume + value
- Top revenue drivers
- People served and people who do the work

## Operational footprint
Business functions the data represents.

## What a downstream LLM should hold in mind
3–5 non-obvious facts about the company's shape.

Rules for the markdown:
- Every number must come from a query you actually ran.
- Use human-readable names from the data, never bare IDs.
- Match vocabulary to the business (a hospital has patients, not customers).
- No meta talk about databases, schemas, queries, or the dataset.
- If a fact isn't queryable, write "Not measured."
- Begin your response with the H1 \`# ${orgName} — Business Profile\`. NOTHING may precede it — no preamble, no code fence, no acknowledgement.

================================================================================
INPUT — the company you must profile:
================================================================================

${JSON.stringify(
  {
    companyName: orgName,
    companyNote,
  },
  null,
  2,
)}
`;
}

export type ProfilerProgress = (note: string) => void;

export type ProfilerResult = {
  businessProfile: string;
};

export async function runProfiler(args: {
  orgId: string;
  mcpUrl: string;
  orgName: string;
  companyNote: string;
  /** processing_job.id — tags Hermes's scratch dir for DB correlation. */
  jobId?: string;
  onProgress?: ProfilerProgress;
  /** Pipe backend stderr to the parent process. Test harness only. */
  debug?: boolean;
}): Promise<ProfilerResult> {
  const { orgId, mcpUrl, orgName, companyNote, jobId, onProgress, debug } = args;

  // 1. Prefetch discovery artifacts from the GraphJin /api/v1 base.
  // GraphJin generates these lazily on first hit — the cold-start compute
  // can exceed undici's idle-socket timeout and surface as "fetch failed",
  // so each request retries with backoff before giving up.
  const apiBase = mcpUrl.replace(/\/mcp\/?$/, "");
  const [tables, insights, syntax] = await Promise.all([
    fetchDiscovery(`${apiBase}/discovery/tables?limit=500`, "tables"),
    fetchDiscovery(`${apiBase}/discovery/insights`, "insights"),
    fetchDiscovery(`${apiBase}/discovery/syntax`, "syntax"),
  ]);
  console.log(
    `[profiler] org=${orgId} tables=${tables.length}B insights=${insights.length}B syntax=${syntax.length}B`,
  );

  // 2. Resolve the configured agent backend (hermes | claude-agent).
  const backend = await resolveAgentBackend(orgId);
  console.log(`[profiler] org=${orgId} backend=${backend.id}`);

  const prompt = buildPrompt({
    orgName,
    companyNote,
    k: { tables, insights, syntax },
    shellTool: shellToolName(backend.id),
  });

  // 3. Run. Backends don't surface per-turn progress, so we emit one
  // "running" beat now and one "drafted" beat at the end.
  if (onProgress) onProgress("Running profiler agent (1–2 minutes)…");
  const startedAt = Date.now();
  const result = await backend.run({
    prompt,
    orgId,
    tag: jobId ?? orgId,
    debug: debug === true,
  });
  if (result.status !== "completed") {
    throw new Error(result.error ?? `${backend.id} returned status=${result.status}`);
  }
  const stdout = result.finalText;
  const elapsedSec = ((Date.now() - startedAt) / 1000).toFixed(0);
  console.log(
    `[profiler] org=${orgId} done in ${elapsedSec}s (${stdout.length} chars)`,
  );

  const businessProfile = stripFences(stdout);
  if (onProgress) onProgress("Profile drafted");

  return { businessProfile };
}

// Both backends sometimes wrap their final reply in a ```markdown fence
// despite prompt instructions. Belt-and-braces: strip a single outermost
// fence if present.
function stripFences(raw: string): string {
  const trimmed = raw.trim();
  const fenced = trimmed.match(/^```(?:markdown|md)?\s*([\s\S]*?)\s*```\s*$/);
  return (fenced?.[1] ?? trimmed).trim();
}
