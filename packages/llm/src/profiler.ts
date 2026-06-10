import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";
import { runValidatedAgentTurn } from "./agent-validate-loop";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack,
  readKnowledgePack,
  type KnowledgePackContents,
} from "./knowledge-pack";
import {
  ensureGraphjinGuard,
  resolveBinaryOnPath,
} from "./work/graphjin-guard";
import { ensureWorkWorkspace } from "./work/workspace";

function buildPrompt(args: {
  orgName: string;
  companyNote: string;
  knowledge: KnowledgePackContents;
  shellTool: string;
}): string {
  const { orgName, companyNote, knowledge, shellTool } = args;
  return `You build a short markdown business profile about a customer company by querying its database via GraphJin.

EXECUTION PATTERN:
1. Read the prefetched GraphJin knowledge sections below before writing any query. They are the authoritative DSL + schema/relationship context for this database. Don't run schema-discovery commands; that context is already prefetched here.
2. Skim the tables + insights sections to identify what this business actually does (industry, offering, business model). Pick the handful of tables that matter.
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
- DO NOT call \`graphjin cli list_tables\` / \`describe_table\` / \`get_query_syntax\` / \`get_schema_insights\` / \`get_discovery_schema\` — those broad discovery dumps are already prefetched in the knowledge sections below.
- DO NOT run \`graphjin cli setup\`, \`graphjin cli config\`, \`graphjin cli write_query\`, or any config/write command. The CLI is already configured by OpenNeko and those commands are blocked.
- DO use these targeted read-only relationship tools whenever they help you plan or verify joins:
  - \`graphjin cli find_path --args '{"from_table":"<table>","to_table":"<table>"}'\` — exact relationship path between two specific tables.
  - \`graphjin cli explore_relationships --args '{"table":"<name>"}'\` — connected tables around one focal table.
- Other useful subcommands: \`graphjin cli explain --args '{"query":"..."}'\` (compile-only, no execution); \`graphjin cli fix_query_error --args '{"query":"...","error":"..."}'\` (get a corrected query); \`graphjin cli health\` (sanity check).
- Never invent data — every number in the profile must trace back to a \`graphjin cli execute_graphql\` response from this run.

QUERY CONSTRUCTION — let the database aggregate:
Prefer one bulk query with server-side aggregation (count, sum, avg) over multiple round-trips that pull rows back to the agent. Specific GraphJin capabilities to reach for (consult \`syntax.json\` for full DSL reference):

- Expression aggregates — sum(expr: {...}), ratio(expr: {...}) — USE THESE FIRST when a fact involves arithmetic across columns (e.g. SUM(price × qty)).
- Joined-column access via dot-notation: { col: "product.standardcost" } works across FKs up to 3 hops.
- For top-N by an aggregate, follow the prefetched syntax limitations. If GraphJin cannot order by an aggregate alias, fetch the grouped aggregate rows and sort the small result set in your reasoning.
- Global single-row aggregate: a top-level select whose fields are ALL aggregates collapses to one row, no distinct needed.

HARD CONSTRAINTS (violating any of these is a critical failure):
- Never hardcode calendar years; compute periods with relative arithmetic. If you need an anchor date, use a recent date from the data.
- For date/range filters, do not put multiple operators under the same column object. Use an explicit \`and\` array:
  \`where: { and: [{ orderdate: { gte: "2024-06-30" } }, { orderdate: { lte: "2025-06-29" } }] }\`
  not \`where: { orderdate: { gte: "...", lte: "..." } }\`.
- Never use a bare limit without pagination. Use cursor-based pagination to process all rows, or use GraphQL aggregation with distinct to let the database aggregate.
- Watch the silent 20-row default limit on every query level (top AND nested) — set explicit limit or use distinct+aggregation.
- Never invent or interpolate. If a query returned no rows, the answer is "Not measured.", not a guess.

================================================================================
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
Syntax — authoritative GraphJin DSL reference (operators, aggregations, pagination):
================================================================================

${knowledge.syntax}

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

  const workspace = await ensureWorkWorkspace(
    orgId,
    "profiler",
    jobId ?? orgId,
  );
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
  const knowledge = await readKnowledgePack(knowledgePackPaths(workspace.knowledgeRoot));

  const backend = await resolveAgentBackend(orgId);
  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    throw new Error("graphjin CLI is not installed on PATH.");
  }
  await ensureGraphjinGuard(workspace.binRoot, graphjinBinary);
  console.log(`[profiler] org=${orgId} backend=${backend.id}`);

  const prompt = buildPrompt({
    orgName,
    companyNote,
    knowledge,
    shellTool: shellToolName(backend.id),
  });

  if (onProgress) onProgress("Running profiler agent (1–2 minutes)…");
  const startedAt = Date.now();
  // GJ2: iterative validation loop — a profile missing required sections
  // (or containing failure text) goes back to the agent for a corrective
  // turn instead of failing the onboarding job.
  const { value: businessProfile, finalText } = await runValidatedAgentTurn({
    backend,
    run: {
      prompt,
      orgId,
      tag: jobId ?? orgId,
      workspace,
      debug: debug === true,
    },
    label: `profiler org=${orgId}`,
    validate: (txt) => validateBusinessProfile(stripFences(txt), orgName),
  });
  const elapsedSec = ((Date.now() - startedAt) / 1000).toFixed(0);
  console.log(
    `[profiler] org=${orgId} done in ${elapsedSec}s (${finalText.length} chars)`,
  );
  if (onProgress) onProgress("Profile drafted");

  return { businessProfile };
}

function stripFences(raw: string): string {
  const trimmed = raw.trim();
  const fenced = trimmed.match(/^```(?:markdown|md)?\s*([\s\S]*?)\s*```\s*$/);
  return (fenced?.[1] ?? trimmed).trim();
}

const REQUIRED_PROFILE_SECTIONS = [
  "## What they are",
  "## Who they serve",
  "## Where they operate",
  "## Scale",
  "## Operational footprint",
  "## What a downstream LLM should hold in mind",
] as const;

const FAILURE_TEXT_RE =
  /\b(i am sorry|unable to connect|restricted network|business_profile\.md|execute the graphql query yourself|graphjin server|could not access|couldn't access|cannot access|no direct access)\b/i;

export function validateBusinessProfile(profile: string, orgName: string): string {
  const expectedHeading = `# ${orgName} — Business Profile`;
  if (!profile.startsWith(expectedHeading)) {
    throw new Error(
      `profiler returned invalid business profile: expected heading "${expectedHeading}"`,
    );
  }
  if (FAILURE_TEXT_RE.test(profile)) {
    throw new Error(
      "profiler returned failure text instead of a business profile",
    );
  }
  for (const section of REQUIRED_PROFILE_SECTIONS) {
    if (!profile.includes(section)) {
      throw new Error(
        `profiler returned invalid business profile: missing section "${section}"`,
      );
    }
  }
  return profile;
}
