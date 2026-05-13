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

function buildDataAccessSection(
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
- ${knowledge.files.syntax} (DSL operators, aggregations, pagination, expression aggregates)

Run queries via the \`${shellTool}\` tool:

  graphjin cli execute_graphql --args '{"query":"<your read-only graphql>"}'

If a response contains an \`errors\` array, run:

  graphjin cli fix_query_error --args '{"query":"<failing>","error":"<msg>"}'

to get a corrected query, then run execute_graphql again.

These targeted read-only relationship tools are also available when
they help you plan or verify joins:

  graphjin cli find_path --args '{"from_table":"<table>","to_table":"<table>"}'
    (exact relationship path between two specific tables)
  graphjin cli explore_relationships --args '{"table":"<name>"}'
    (connected tables around one focal table)

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

const RULES_SECTION = `<rules>
- Keep answers concise and useful.
- For GraphJin date/range filters, do not put multiple operators under
  the same column object. Use
  \`where: { and: [{ orderdate: { gte: "2024-06-30" } },
                   { orderdate: { lte: "2025-06-29" } }] }\`
  rather than \`where: { orderdate: { gte: "...", lte: "..." } }\`.
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
