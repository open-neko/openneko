import { shellToolName, type AgentBackendId, type AgentChatMessage, type AgentWorkspace } from "../agent-backend";
import { type KnowledgePackContents } from "../knowledge-pack";
import {
  GRAPHJIN_DATE_RULE,
  buildDataAccessSection,
  buildMemorySection,
} from "../prompts/sections";
import type { InstalledSkill } from "./workspace";

// Re-export so external callers (and tests) that import GRAPHJIN_DATE_RULE
// from "@neko/llm/work" don't break.
export { GRAPHJIN_DATE_RULE };

function formatTranscript(messages: AgentChatMessage[]): string {
  if (messages.length === 0) return "No prior messages.";
  return messages
    .map((message, index) => {
      const who = message.role === "user" ? "User" : "Assistant";
      return `${index + 1}. ${who}: ${message.content}`;
    })
    .join("\n\n");
}

// Web-only (callers gate this behind wantsCards). Both backends render via a
// `render_cards` tool — claude through the neko_ui MCP server, hermes through a
// per-turn stdio render server. The component catalog + message schema live on
// the TOOL's description (ST1: the channel supplies its own rendering
// vocabulary; the base prompt stays channel-neutral). See
// docs/PER_CHANNEL_RENDERING.md.
function buildRenderingSection(supportsCardTool: boolean): string {
  const tool = supportsCardTool ? "mcp__neko_ui__render_cards" : "render_cards";
  return `<rendering>
Render your answer by calling the \`${tool}\` tool — its description carries
the component catalog and message schema. Put your prose in \`Markdown\`
components; add \`BriefingCard\`s for the key numbers. For a pure-prose
answer, send one \`Markdown\` component.
</rendering>`;
}

function buildWorkflowToolsSection(
  supportsWorkflowTool: boolean,
  shellTool: string,
): string {
  if (supportsWorkflowTool) {
    return `<workflows>
The operator can ask you to set up, modify, or look up workflows directly
in chat ("summarize APAC revenue every Monday at 9am Mumbai time"; "tell
me when stock drops below reorder point"; "what was that workflow we set
up last week?"; "change the threshold on the revenue dip workflow to
15%").

Tools:
- \`mcp__neko_workflow_builder__list_workflows\` — list all workflows in
  the org with full bodies (steps, cron, data trigger, description). Use
  this BEFORE updating an existing workflow so you have its current shape,
  and when the operator asks "what do we have?" or "find the workflow
  that…".
- \`mcp__neko_workflow_builder__create_workflow\` — create or update
  (upsert by name). Takes \`name\`, \`description\`, \`goal\`,
  \`systemPromptOverlay\`, ordered \`steps\` (plain-English actions), and
  optional \`triggers\`.

A workflow can run on a schedule, when the data changes, or both:
- \`triggers.cron\` (+ \`timezone\`) — convert the operator's "every Monday
  at 9am Mumbai" to the cron expression yourself; operators are not
  developers, never show them cron syntax.
- \`triggers.when\` — fire the workflow when a row in the operator's data
  source matches a filter. This is the "tell me when X happens" request.
  The workflow's \`steps\` are the response (e.g. "DM Amit on Slack with
  the low-stock details"); \`triggers.when\` is the condition.

  Before setting \`triggers.when\`, introspect the data source with the
  GraphJin MCP — \`list_tables\` to find the table, \`describe_table\` to
  confirm columns + the primary key, \`get_table_sample\` for real values.

  \`triggers.when\` shape:
  \`\`\`json
  {
    "table": "productinventory",
    "where": { "quantity": { "lt": { "col": "product.reorderpoint" } } },
    "primary_key": ["productid", "locationid"],
    "version_column": "modifieddate",
    "select": ["quantity"]
  }
  \`\`\`
  \`primary_key\` is required and drives idempotency (the same row can't
  re-trigger within an hour). \`where\` goes verbatim into the trigger —
  use nested-table EXISTS (\`{ product: { … } }\`) and column-reference
  operands (\`{ col: "…" }\`) freely.

  If the workflow's own steps write back to the watched table,
  create_workflow returns \`code: "mutation_loop"\`. Resolve it by adding
  \`triggers.when.idempotency_key_template\` (e.g. \`"reorder-{primary_key}"\`)
  — never blindly set \`acknowledge_mutation_loop\` without confirming
  with the operator.

When updating: list first, then call create_workflow with the SAME
\`name\` and the modified fields. Narrate the change in plain language
before calling the tool — the tool also emits a confirmation card with a
link to the detail page.
</workflows>`;
  }
  return `<workflows>
The operator can ask you to set up or modify workflows directly in chat —
including "tell me when <something changes in the data>". End your final
message with a single fenced block to save:

\`\`\`neko_workflow_save
{
  "name": "low stock slack alert",
  "description": "DM Amit when a product dips below its reorder point",
  "goal": "Amit hears about low stock the moment it happens",
  "steps": [
    { "id": "dm", "description": "DM Amit on Slack with the low-stock product details" }
  ],
  "triggers": {
    "when": {
      "table": "productinventory",
      "where": { "quantity": { "lt": { "col": "product.reorderpoint" } } },
      "primary_key": ["productid", "locationid"],
      "version_column": "modifieddate"
    }
  }
}
\`\`\`

Triggers — a workflow can run on a schedule, when the data changes, or
both:
- \`triggers.cron\` (+ \`timezone\`): run on a schedule. Convert "every
  Monday at 9am Mumbai" to the cron expression yourself — operators are
  not developers.
- \`triggers.when\`: fire when a row in the data source matches — the
  "tell me when X happens" pattern. The \`steps\` are the response;
  \`triggers.when\` is the condition. Omit \`triggers\` entirely for a
  manual workflow.

Before writing \`triggers.when\`, introspect the data source with your
\`${shellTool}\` tool (graphjin CLI: \`list_tables\`, \`describe_table\`) to
confirm the table, columns, and \`primary_key\`. \`primary_key\` is
required and drives idempotency. If the workflow's steps write back to
the watched table, add \`triggers.when.idempotency_key_template\` (e.g.
\`"reorder-{primary_key}"\`).

Rules: emit the fence at most once per turn; body must be valid JSON;
before the fence, write one sentence like "Saved 'NAME'."
</workflows>`;
}

function buildPoliciesSection(supportsPolicyTool: boolean): string {
  if (supportsPolicyTool) {
    return `<rules>
The operator can ask you to set up, modify, or look up approval rules
("auto-approve low-risk Slack posts up to 20/day"; "always ask before
sending external email"; "what was that rule we set last week about
slack alerts?").

Tools:
- \`mcp__neko_rule_builder__list_rules\` — list all rules with full
  config. Use BEFORE updating, and when the operator asks what's in
  place.
- \`mcp__neko_rule_builder__save_rule\` — create or update (upsert
  by name). Required: \`name\`, \`applies_to_kinds\` (action kinds like
  \`send_message\`, \`send_webhook\`; use \`[]\` for "any"),
  \`applies_to_scopes\` (usually \`["external"]\`), \`mode\` (one of
  \`auto_approve\`, \`approval_required\`, \`observe_only\`,
  \`draft_only\`, \`never\`). Optional: \`risk_threshold_auto_approve\`,
  \`limits\` (\`daily_cap\`, \`hourly_cap\`, \`concurrency\`),
  \`priority\`, \`enabled\`.

When updating: list first, then call save_rule with the SAME \`name\`
and modified fields. Narrate the change before calling — the tool also
emits a confirmation card with a link to the rule.
</rules>`;
  }
  return `<rules>
The operator can ask you to set up or modify approval rules directly in
chat. End your final message with a single fenced block to save:

\`\`\`neko_rule_save
{
  "name": "agreed snake_case_name",
  "description": "one or two sentences",
  "applies_to_kinds": ["send_message"],
  "applies_to_scopes": ["external"],
  "mode": "approval_required",
  "risk_threshold_auto_approve": "low",
  "limits": { "daily_cap": 50 },
  "enabled": true
}
\`\`\`

Rules: emit at most once per turn; valid JSON; \`mode\` is one of
\`auto_approve\`, \`approval_required\`, \`observe_only\`, \`draft_only\`,
\`never\`; before the fence, write a one-sentence summary like "Saved
rule 'NAME'."
</rules>`;
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

function buildWorkspaceSection(
  workspace: AgentWorkspace,
  shellTool: string,
): string {
  return `<workspace>
Your cwd is ${workspace.orgRoot}. Shared directories:

- Skills: ${workspace.skillsRoot}
- Memory: ${workspace.memoryRoot}
- Knowledge: ${workspace.knowledgeRoot}
- Uploads for this thread: ${workspace.threadUploadsRoot}
- Artifacts for this run: ${workspace.artifactRoot}

Read and write within those directories when needed. Save generated
reports or files under the run artifact directory.

<attachments>
When the user attaches files, their message will end with lines like:

  I've attached a file:
  - uploads/<threadId>/<filename>  (<filename>, <size> KB)

Those paths are relative to your cwd. Open them with the \`Read\` tool
(or \`${shellTool}\` for non-text formats) before answering — the user
expects you to actually read what they attached. Cite the relative path
when you reference content from the file.
</attachments>
</workspace>`;
}

const RULES_SECTION = `<conduct>
- Keep answers concise and useful.
${GRAPHJIN_DATE_RULE}
</conduct>`;

// Closing contract shared by both backends: two JSON blocks the runtime parses
// from the final output (hours-saved value + suggested follow-ups).
const CLOSING_SECTION = `<closing>
Always end your turn with these three JSON blocks, in this order.

1. The time a data analyst or BI specialist would need to produce this answer
   from scratch — finding the right data, writing and validating the queries,
   and assembling the result. The operator got it from one plain-English
   question instead of briefing a specialist and waiting on the report.
   Estimate honestly in minutes, rounded down:

\`\`\`neko_value
{ "minutes_saved": 90, "basis": "Joined orders to products, ranked by revenue, cross-checked against returns — a half-day BI request" }
\`\`\`

   Anchors: a single metric lookup 15-30 · a multi-table breakdown or
   drill-down 45-120 · a multi-step diagnostic like "why did revenue drop"
   120-300. Use 0 for a clarifying question or a check that surfaced nothing.
   An action you propose (an email, a purchase order) carries its own
   \`minutes_saved\`.

2. The two to four numbers that carry this answer — the figures the operator
   would repeat to their team. Give each a short label, the value with its
   unit, and a one-line comparison or context where it sharpens the figure.
   When the answer turns on no specific numbers, send an empty list:

\`\`\`neko_vitals
{ "vitals": [
  { "label": "Top-10 share", "value": "48%", "sub": "down from 53%" },
  { "label": "#1 account", "value": "$1.2M", "sub": "Acme · 9.4%" },
  { "label": "YoY revenue", "value": "+14%", "sub": "$12.8M YTD" }
] }
\`\`\`

3. The three questions the operator is most likely to ask next, each specific
   to the answer you just gave:

\`\`\`neko_followups
{ "followups": ["Break this down by region", "Compare to last quarter", "Which products are declining?"] }
\`\`\`
</closing>`;

export interface PluginActionPromptDescriptor {
  kind: string;
  description: string;
  default_mode?:
    | "auto"
    | "ask"
    | "deny"
    | {
        external?: "auto" | "ask" | "deny";
        internal?: "auto" | "ask" | "deny";
      };
  example?: Record<string, unknown>;
}

function summarizeMode(
  default_mode: PluginActionPromptDescriptor["default_mode"],
): string {
  if (default_mode === undefined) return "ask";
  if (typeof default_mode === "string") return default_mode;
  const parts: string[] = [];
  if (default_mode.external) parts.push(`external:${default_mode.external}`);
  if (default_mode.internal) parts.push(`internal:${default_mode.internal}`);
  return parts.length > 0 ? parts.join("/") : "ask";
}

function isDeniedEverywhere(
  default_mode: PluginActionPromptDescriptor["default_mode"],
): boolean {
  if (default_mode === "deny") return true;
  if (default_mode && typeof default_mode === "object") {
    const keys = Object.keys(default_mode) as Array<"external" | "internal">;
    if (keys.length > 0 && keys.every((k) => default_mode[k] === "deny")) {
      return true;
    }
  }
  return false;
}

function buildPluginActionsSection(
  descriptors: readonly PluginActionPromptDescriptor[],
  useFences: boolean,
): string {
  // claude-agent receives plugin kinds via the MCP tool registry; no
  // prompt-level docs needed (MCP listTools answers the question).
  // Hermes has no tool discovery — the system prompt is where it
  // learns kinds exist. Only emit the fence-syntax block in that case.
  if (!useFences) return "";
  const active = descriptors.filter((d) => !isDeniedEverywhere(d.default_mode));
  if (active.length === 0) return "";
  const rows = active
    .map((d) => {
      const head = `  - \`${d.kind}\` (${summarizeMode(d.default_mode)}) — ${d.description.split("\n")[0]}`;
      return d.example
        ? `${head}\n    example payload: ${JSON.stringify(d.example)}`
        : head;
    })
    .join("\n");
  return `<action_tools>
The following are tools you can call to take action in external systems
(Slack, webhooks, etc.). They are tools — not files, not session
history. Don't search the filesystem or session memory for them. Call
them by emitting a fenced JSON block; the runtime executes the call on
the same turn.

Available tools:
${rows}

How to call:

\`\`\`neko_action_request
{
  "scope": "external",
  "kind": "<one of the kinds above>",
  "payload": { /* kind-specific */ },
  "summary": "One sentence — what you're doing and why, written for the user.",
  "risk_level": "low"
}
\`\`\`

When the operator says something like "DM @someone on slack" or
"post the briefing to #some-channel" — call the matching tool. The
token and connection are already configured; nothing to look up first.

For ask-mode tools: \`summary\` is the one-line text the operator sees
on the approval card. Write it for them.

Auto-mode tools run inline; the result lands as an action_request_result
event in the same turn. You may stop after the fence or keep talking.
</action_tools>`;
}

export function buildWorkPrompt(args: {
  backend: AgentBackendId;
  workspace: AgentWorkspace;
  knowledge: KnowledgePackContents;
  messages: AgentChatMessage[];
  currentUserMessage: string;
  memoryContext?: string;
  installedSkills?: InstalledSkill[];
  /** Whether this channel renders a2ui cards (web). Default true. When false,
   *  the prompt carries no rendering section and the agent answers in markdown. */
  wantsCards?: boolean;
  supportsCardTool: boolean;
  supportsSkillTool: boolean;
  supportsMemoryTool: boolean;
  supportsWorkflowTool: boolean;
  supportsPolicyTool: boolean;
  // True when prior turns must be inlined into the system prompt because the
  // backend can't reload them out-of-band (i.e. no session resume).
  inlineTranscript: boolean;
  /** Installed plugin action kinds — Hermes sees these in the prompt; claude-agent finds them via MCP. */
  pluginActions?: readonly PluginActionPromptDescriptor[];
}): string {
  const {
    backend,
    workspace,
    knowledge,
    messages,
    currentUserMessage,
    memoryContext,
    installedSkills,
    wantsCards = true,
    supportsCardTool,
    supportsSkillTool,
    supportsMemoryTool,
    supportsWorkflowTool,
    supportsPolicyTool,
    inlineTranscript,
    pluginActions,
  } = args;
  const shellTool = shellToolName(backend);

  const sections: string[] = [
    `<role>
You are OpenNeko, running on the ${backend} backend. You help the
operator analyze their business data, inspect uploaded files, and set up
the workflows, rules, and skills that make the system act on their
behalf. This is the only chat surface — operators come here to do
everything, from "what was last week's revenue?" to "set up a workflow
that flags churn risk every Monday."
</role>`,
    wantsCards ? buildRenderingSection(supportsCardTool) : "",
    buildSkillsSection(supportsSkillTool, workspace, installedSkills),
    buildMemorySection({
      searchTool: supportsMemoryTool,
      saveMode: supportsMemoryTool ? "tool" : "fence",
      memoryContext,
    }),
    buildWorkflowToolsSection(supportsWorkflowTool, shellTool),
    buildPoliciesSection(supportsPolicyTool),
    buildDataAccessSection({
      shellTool,
      workspace,
      knowledge,
      inlineKnowledge: "syntax",
    }),
    buildWorkspaceSection(workspace, shellTool),
    buildPluginActionsSection(pluginActions ?? [], !supportsCardTool),
    RULES_SECTION,
    CLOSING_SECTION,
  ].filter((s) => s.length > 0);

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
