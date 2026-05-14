import { shellToolName, type AgentBackendId, type AgentChatMessage, type AgentWorkspace } from "../agent-backend";
import { knowledgePackPaths } from "../knowledge-pack";
import type { InstalledSkill } from "./workspace";

function formatTranscript(messages: AgentChatMessage[]): string {
  if (messages.length === 0) return "No prior messages.";
  return messages
    .map((message, index) => {
      const who = message.role === "user" ? "User" : "Assistant";
      return `${index + 1}. ${who}: ${message.content}`;
    })
    .join("\n\n");
}

const A2UI_FENCE_EXAMPLE = `\`\`\`neko_a2ui
[
  {"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"urn:app:catalog:briefing:v1"}},
  {"version":"v0.9","updateComponents":{"surfaceId":"s1","components":[
    {"id":"intro","component":"Markdown","text":"Brief 1-2 sentence intro to the answer."},
    {"id":"card1","component":"BriefingCard","metricId":"top-product","source":"chat","mood":"good","text":"Mountain-200 leads","metric":"$674,216","label":"Total Profit","detail":"Across all sales channels.","chartType":"kpi","chartData":[]},
    {"id":"detail","component":"Markdown","text":"Optional follow-up prose with tables, lists, etc."}
  ]}}
]
\`\`\``;

function buildRenderingSection(supportsCardTool: boolean): string {
  if (supportsCardTool) {
    return `<rendering>
Every response to the user goes through \`mcp__neko_ui__render_cards\`.
Wrap your prose in a \`Markdown\` component, and add KPI/Table/Chart
cards alongside it when structured data helps the answer. Anything
written outside the tool call is invisible to the UI — the tool call is
the response.
</rendering>`;
  }

  return `<rendering>
Every response to the user is a single fenced \`\`\`neko_a2ui block
containing A2UI v0.9 JSON messages. Anything written outside the fence
is invisible to the UI — the fence is the entire response.

The fence body is a JSON array (not JSX, not HTML, not bare component
objects). Components are emitted flat inside
\`updateComponents.components\` — every component is at the top level of
that array, never nested inside another component's \`children\`.

Component catalog (every component has a \`component\` field set to one
of these):

- \`Markdown\` — narrative text. Props: \`{ text: string }\` (markdown).
  Use this for any prose.
- \`BriefingCard\` — KPI card. Props:
  \`{ metricId: string, source: 'chat', mood: 'good'|'watch'|'act',
     text: string, metric: string, label: string, detail: string,
     chartType: 'kpi'|'line'|'bar'|'area'|'donut',
     chartData: Array<{d:string,v:number,t?:number}> | [] }\`.

Each message has \`version: "v0.9"\` plus exactly one of
\`createSurface\` or \`updateComponents\`. Most responses need just one
of each.

<example>
${A2UI_FENCE_EXAMPLE}
</example>

When the answer is purely prose with no metrics or cards, emit a single
\`Markdown\` component inside \`updateComponents\`.
</rendering>`;
}

function buildSkillsSection(
  supportsSkillTool: boolean,
  workspace: AgentWorkspace,
  installedSkills: InstalledSkill[] | undefined,
): string {
  const skillList =
    installedSkills && installedSkills.length > 0
      ? installedSkills
          .map(
            (s) =>
              `- ${s.name} — ${s.description || `details in ${workspace.skillsRoot}/${s.name}/SKILL.md`}`,
          )
          .join("\n")
      : `(none installed; check ${workspace.skillsRoot})`;

  const creationGuidance = supportsSkillTool
    ? `When the user asks you to create or update a skill, use
\`mcp__neko_skills__create_skill\`.`
    : `When the user asks you to create or update a skill, write
agentskills.io-style files into the shared skills directory shown above
using your shell tool (e.g. \`mkdir -p\` + \`cat > SKILL.md\`). Skills
only appear in the OpenNeko sidebar when files land at that path —
Hermes' built-in \`skill_manage\` / \`skills_list\` / \`skill_view\`
tools write to a private directory the UI doesn't read, so anything
saved there is invisible to the user.`;

  return `<skills>
Installed skills — capability recipes you can use. Before telling the
user you cannot do something, check whether one of these skills covers
it and read its SKILL.md for usage details. The host image ships
Python 3, LibreOffice (\`soffice\`), Poppler (\`pdftotext\`), qpdf, plus
pip libs: pypdf, pdfplumber, reportlab, Pillow, openpyxl, python-pptx,
python-docx, PyYAML.

${skillList}

${creationGuidance}
</skills>`;
}

function buildMemorySection(
  supportsMemoryTool: boolean,
  memoryContext: string | undefined,
): string {
  const loaded = memoryContext?.trim()
    ? memoryContext.trim()
    : "No core memories are currently saved for this workspace or thread.";

  const usage = supportsMemoryTool
    ? `Long-term memory is available through \`mcp__neko_memory__search\`,
\`mcp__neko_memory__remember\`, and \`mcp__neko_memory__forget\`.

Search memory when the user asks about prior decisions, preferences,
recurring metric definitions, business rules, company context, or older
thread context.

Save memory only for explicit durable instructions, corrections, or
preferences. Skip ordinary one-off filters or analysis results.

When saving memory, use \`global\` scope unless the user says it's only
for this Work thread; in that case use \`thread\`.`
    : `Core memory is shown above. If the user explicitly asks you to
remember or forget something, explain that durable memory writes require
the Claude Agent backend. Hermes' built-in \`memory\` tool writes to a
private directory the OpenNeko UI doesn't read from, so anything saved
there is invisible to the user.`;

  return `<long_term_memory>
${loaded}

${usage}
</long_term_memory>`;
}

export function buildDataAccessSection(
  shellTool: string,
  workspace: AgentWorkspace,
): string {
  const knowledge = knowledgePackPaths(workspace.knowledgeRoot);
  return `<data_access>
The configured GraphJin database is the authoritative source for any
operational question (revenue, customers, orders, inventory, employees,
sales, products, etc.). Uploaded files are auxiliary — use them only
when the user explicitly references them ("in the file I just uploaded")
or the database genuinely doesn't have what they're asking for.

Read these prefetched knowledge files with your \`${shellTool}\` tool
before writing any query. Broad schema and syntax dumps are already on
disk; calling \`graphjin cli list_tables\` / \`describe_table\` /
\`get_query_syntax\` / \`get_schema_insights\` / \`get_discovery_schema\`
duplicates work that's already done:

- ${knowledge.files.index} (start here — index of the rest)
- ${knowledge.files.tables} (every table, schema, column count)
- ${knowledge.files.namespaces} (multi-DB namespace routing, if any)
- ${knowledge.files.insights} (hub tables, hot relationships, relationship paths, query templates)
- ${knowledge.files.syntax} (DSL operators, aggregations, pagination, expression aggregates, common mistakes)

When the question involves any aggregation (totals, top-N, averages,
revenue, margin, share), READ \`syntax.json\` before writing the query.
The aggregation rules below are a precis; the full reference lives in
that file.

QUERY CONSTRUCTION — let the database aggregate. Prefer one bulk query
with server-side aggregation (count, sum, avg) over multiple round-trips
that pull rows back to the agent:

- Aggregation fields use the pattern \`<fn>_<column>\`: count_id, sum_price,
  avg_quantity, min_x, max_x. There are NO \`<table>_sum\` tables — that
  pattern does not exist.
- GROUP BY does not exist. Use \`distinct: [columns]\` for grouping.
- Expression aggregates — \`sum(expr: {...})\`, \`ratio(expr: {...})\` —
  USE THESE when the metric involves arithmetic across columns
  (e.g. revenue = SUM(price × qty), margin %). Multiplying single-column
  aggregates is mathematically wrong: \`avg_price × sum_quantity\` ≠
  \`sum(price × quantity)\`.
- Joined-column access via dot-notation works across FKs up to 3 hops:
  \`{ col: "product.standardcost" }\` inside an expression unlocks
  revenue × cost calculations in one server-side aggregate.
- For top-N by an aggregate, fetch the grouped aggregate rows and sort
  the small result in reasoning if GraphJin can't order by the alias.
- Global single-row aggregate: a top-level select whose fields are ALL
  aggregates collapses to one row, no \`distinct\` needed.
- Watch the silent 20-row default limit on every query level (top AND
  nested) — set an explicit limit or use \`distinct + aggregation\`.
  If you pull raw rows and aggregate in your head, you almost certainly
  only saw the first page; the totals will be wrong by orders of
  magnitude. Aggregate in the database.
- ${GRAPHJIN_DATE_RULE.replace(/^- /, "")}
- Never invent or interpolate. If a query returned no rows, the answer
  is "no data", not a guess.

WORKED AGGREGATION EXAMPLES — copy these shapes; substitute your real
table and column names. These are the patterns GraphJin actually
accepts (deviating from them produces "expecting an aliased field
name" or "<table>_sum table not found" errors):

  // Global single-row total — no distinct needed, fields are all aggregates:
  { sales_orders { total_revenue: sum(expr: { mul: [unitprice, quantity] }) } }

  // Group by category, server-side SUM(price × qty), ranked top-N:
  { sales_orders(distinct: [category_id], order_by: { revenue: desc }, limit: 10) {
      category_id
      revenue: sum(expr: { mul: [unitprice, quantity] })
    } }

  // Joined column via FK dot-notation (up to 3 hops) — gross margin from a related table:
  { sales_orders(distinct: [product_id]) {
      product_id
      gross: sum(expr: { mul: [quantity, { sub: [unitprice, "product.standardcost"] }] })
    } }

  // Ratio of aggregates — bare expression, nested sum/avg nodes:
  { sales_orders { margin_pct: ratio(expr: { div: [{ sum: { mul: [unitprice, quantity] } }, { sum: linetotal }] }) } }

  // Plain single-column aggregates — no expression needed for one column:
  { products(distinct: [category_id]) { category_id count_id sum_price avg_price } }

For grouping ACROSS a relationship (e.g. revenue by category when the
order rows live in a child table and the category lives upstream):
use a foreign-key dot-path inside \`distinct:\` and select the grouping
key as a separate aggregated row. If that fails with "aliased field
name" errors, GraphJin can't group across that path in one query —
run the aggregate per parent in a small fan-out loop instead, NOT
fall back to raw row pulls.

Run queries via the \`${shellTool}\` tool:

  graphjin cli execute_graphql --args '{"query":"<your read-only graphql>"}'

If a response contains an \`errors\` array, run:

  graphjin cli fix_query_error --args '{"query":"<failing>","error":"<msg>"}'

to get a corrected query, then run execute_graphql again.

These targeted read-only relationship tools are also available when
they help you plan or verify joins:

  graphjin cli find_path --args '{"from_table":"<table>","to_table":"<table>"}'
  graphjin cli explore_relationships --args '{"table":"<name>"}'

Talk to GraphJin only through \`${shellTool}\` running \`graphjin cli\`.
\`execute_code\`, Python, raw HTTP, or any other path bypasses the tool
gate that blocks mutations and subscriptions, and produces results the
rest of the system can't trace. Mutations and subscriptions are blocked
at the tool gate regardless.
</data_access>`;
}

function buildWorkspaceSection(workspace: AgentWorkspace): string {
  return `<workspace>
Shared directories:

- Skills: ${workspace.skillsRoot}
- Memory: ${workspace.memoryRoot}
- Knowledge: ${workspace.knowledgeRoot}
- Uploads for this thread: ${workspace.threadUploadsRoot}
- Artifacts for this run: ${workspace.artifactRoot}

Read and write within those directories when needed. Save generated
reports or files under the run artifact directory.
</workspace>`;
}

export const GRAPHJIN_DATE_RULE = `- For GraphJin date/range filters, do not put multiple operators under
  the same column object. Use
  \`where: { and: [{ orderdate: { gte: "2024-06-30" } },
                   { orderdate: { lte: "2025-06-29" } }] }\`
  rather than \`where: { orderdate: { gte: "...", lte: "..." } }\`.`;

const RULES_SECTION = `<rules>
- Keep answers concise and useful.
${GRAPHJIN_DATE_RULE}
</rules>`;

export function buildWorkPrompt(args: {
  backend: AgentBackendId;
  workspace: AgentWorkspace;
  messages: AgentChatMessage[];
  currentUserMessage: string;
  memoryContext?: string;
  installedSkills?: InstalledSkill[];
  supportsCardTool: boolean;
  supportsSkillTool: boolean;
  supportsMemoryTool: boolean;
  // True when prior turns must be inlined into the system prompt because the
  // backend can't reload them out-of-band (i.e. no session resume).
  inlineTranscript: boolean;
}): string {
  const {
    backend,
    workspace,
    messages,
    currentUserMessage,
    memoryContext,
    installedSkills,
    supportsCardTool,
    supportsSkillTool,
    supportsMemoryTool,
    inlineTranscript,
  } = args;
  const shellTool = shellToolName(backend);

  const sections: string[] = [
    `<role>
You are OpenNeko, running on the ${backend} backend. You help the user
analyze their business data, inspect uploaded files, and create durable
skills or artifacts when useful.
</role>`,
    buildRenderingSection(supportsCardTool),
    buildSkillsSection(supportsSkillTool, workspace, installedSkills),
    buildMemorySection(supportsMemoryTool, memoryContext),
    buildDataAccessSection(shellTool, workspace),
    buildWorkspaceSection(workspace),
    RULES_SECTION,
  ];

  if (inlineTranscript) {
    sections.push(
      `<conversation_so_far>
${formatTranscript(messages)}
</conversation_so_far>

<current_user_message>
${currentUserMessage}
</current_user_message>`,
    );
  }

  return sections.join("\n\n");
}
