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

const RULES_SECTION = `<rules>
- Keep answers concise and useful.
${GRAPHJIN_DATE_RULE}
</rules>`;

export interface PluginActionPromptDescriptor {
  kind: string;
  description: string;
  default_mode?: "auto" | "ask" | "deny";
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
  const active = descriptors.filter((d) => d.default_mode !== "deny");
  if (active.length === 0) return "";
  const rows = active
    .map((d) => {
      const mode =
        d.default_mode === "auto"
          ? "auto"
          : d.default_mode === "ask"
            ? "ask"
            : "ask";
      return `  - \`${d.kind}\` (${mode}) — ${d.description.split("\n")[0]}`;
    })
    .join("\n");
  return `<plugin_actions>
Plugins contribute action kinds you can request inline. Emit each as
a fenced JSON block; the runtime catches the fence, evaluates the
operator's rules, and either fires it (auto) or asks the user to
approve (ask). The fence body must be valid JSON.

\`\`\`neko_action_request
{
  "scope": "external",
  "kind": "<one of the kinds below>",
  "payload": { /* kind-specific */ },
  "summary": "One sentence — what you're doing and why, written for the user.",
  "risk_level": "low"
}
\`\`\`

Installed kinds:
${rows}

For ask-mode kinds: set \`summary\` to a single clear sentence the
operator will read on the approval card. It's how they decide.

Auto-mode kinds run inline; the outcome lands as an
action_request_result event in the same turn — you may stop after
the fence and let the runtime narrate, or continue the conversation
naturally.
</plugin_actions>`;
}

export function buildWorkPrompt(args: {
  backend: AgentBackendId;
  workspace: AgentWorkspace;
  knowledge: KnowledgePackContents;
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
    supportsCardTool,
    supportsSkillTool,
    supportsMemoryTool,
    inlineTranscript,
    pluginActions,
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
    buildMemorySection({
      searchTool: supportsMemoryTool,
      saveMode: supportsMemoryTool ? "tool" : "fence",
      memoryContext,
    }),
    buildDataAccessSection({
      shellTool,
      workspace,
      knowledge,
      inlineKnowledge: "syntax",
    }),
    buildWorkspaceSection(workspace, shellTool),
    buildPluginActionsSection(pluginActions ?? [], !supportsCardTool),
    RULES_SECTION,
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
