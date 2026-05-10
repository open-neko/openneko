import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack,
  type KnowledgePackPaths,
} from "./knowledge-pack";
import { ensureOrgWorkspace } from "./work/workspace";

function buildPrompt(args: {
  orgName: string;
  companyNote: string;
  knowledge: KnowledgePackPaths;
  shellTool: string;
}): string {
  const { orgName, companyNote, knowledge, shellTool } = args;
  return `You build a short markdown business profile about a customer company by querying its database via GraphJin.

EXECUTION PATTERN:
1. Read the GraphJin knowledge pack on disk before writing any query — its INDEX.md tells you which file covers what. Don't run any schema-discovery commands; that information is already on disk.

   Knowledge files (read with your \`${shellTool}\` tool, e.g. \`cat\`):
   - ${knowledge.files.index} (start here — index of the rest)
   - ${knowledge.files.tables} (every table, schema, column count)
   - ${knowledge.files.namespaces} (multi-DB namespace routing)
   - ${knowledge.files.insights} (hub tables, hot relationships, query templates, data-quality flags)
   - ${knowledge.files.syntax} (DSL operators, aggregations, pagination, expression aggregates)

2. Skim tables.json + insights.json to identify what this business actually does (industry, offering, business model). Pick the handful of tables that matter.
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
- DO NOT call \`graphjin cli list_tables\`, \`describe_table\`, \`get_query_syntax\`, etc. — that info is on disk in the knowledge files listed in step 1.
- Other useful subcommands: \`graphjin cli explain --args '{"query":"..."}'\` (compile-only, no execution); \`graphjin cli health\` (sanity check).
- Never invent data — every number in the profile must trace back to a \`graphjin cli execute_graphql\` response from this run.

QUERY CONSTRUCTION — let the database aggregate:
Prefer one bulk query with server-side aggregation (count, sum, avg) over multiple round-trips that pull rows back to the agent. Specific GraphJin capabilities to reach for (consult \`syntax.json\` for full DSL reference):

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
  jobId?: string;
  onProgress?: ProfilerProgress;
  debug?: boolean;
}): Promise<ProfilerResult> {
  const { orgId, mcpUrl, orgName, companyNote, jobId, onProgress, debug } = args;

  const workspace = await ensureOrgWorkspace(orgId);
  const refresh = await prefetchKnowledgePack({
    discoveryUrl: discoveryUrlFromMcpUrl(mcpUrl),
    destDir: workspace.knowledgeRoot,
  });
  if (refresh.ok) {
    const totalBytes = refresh.files.reduce((n, f) => n + f.bytes, 0);
    console.log(
      `[profiler] org=${orgId} knowledge refreshed (${refresh.files.length} files, ${totalBytes}B)`,
    );
  } else {
    console.warn(
      `[profiler] org=${orgId} knowledge refresh failed (${refresh.error}); proceeding with on-disk pack`,
    );
  }
  const knowledge = knowledgePackPaths(workspace.knowledgeRoot);

  const backend = await resolveAgentBackend(orgId);
  console.log(`[profiler] org=${orgId} backend=${backend.id}`);

  const prompt = buildPrompt({
    orgName,
    companyNote,
    knowledge,
    shellTool: shellToolName(backend.id),
  });

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

function stripFences(raw: string): string {
  const trimmed = raw.trim();
  const fenced = trimmed.match(/^```(?:markdown|md)?\s*([\s\S]*?)\s*```\s*$/);
  return (fenced?.[1] ?? trimmed).trim();
}
