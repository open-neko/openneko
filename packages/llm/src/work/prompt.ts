import { shellToolName, type AgentBackendId, type AgentChatMessage, type AgentWorkspace } from "../agent-backend";
import { knowledgePackPaths } from "../knowledge-pack";

function formatTranscript(messages: AgentChatMessage[]): string {
  if (messages.length === 0) return "No prior messages.";
  return messages
    .map((message, index) => {
      const who = message.role === "user" ? "User" : "Assistant";
      return `${index + 1}. ${who}: ${message.content}`;
    })
    .join("\n\n");
}

export function buildWorkPrompt(args: {
  backend: AgentBackendId;
  workspace: AgentWorkspace;
  messages: AgentChatMessage[];
  currentUserMessage: string;
  memoryContext?: string;
  supportsCardTool: boolean;
  supportsSkillTool: boolean;
  supportsMemoryTool: boolean;
}): string {
  const {
    backend,
    workspace,
    messages,
    currentUserMessage,
    memoryContext,
    supportsCardTool,
    supportsSkillTool,
    supportsMemoryTool,
  } =
    args;
  const shellTool = shellToolName(backend);
  const knowledge = knowledgePackPaths(workspace.knowledgeRoot);

  const cardInstructions = supportsCardTool
    ? [
        "Use normal chat prose for ordinary answers.",
        "Use `mcp__neko_ui__render_cards` only when structured Neko cards would help, such as KPIs, tables, charts, or dashboard-style summaries.",
        "After calling `mcp__neko_ui__render_cards`, still write a 1-3 sentence prose summary.",
      ].join(" ")
    : [
        "Use normal chat prose for ordinary answers.",
        "When structured cards would help, include a fenced ```neko_a2ui block containing a JSON array of A2UI v0.9 messages, then follow it with a short prose summary.",
      ].join(" ");

  const skillInstructions = supportsSkillTool
    ? "When the user asks you to create or update a skill, prefer `mcp__neko_skills__create_skill`."
    : [
        "When the user asks you to create or update a skill, write agentskills.io-style files into the shared skills directory shown below using your shell tool (e.g. `mkdir -p` + `cat > SKILL.md`).",
        "DO NOT use Hermes' built-in `skill_manage` / `skills_list` / `skill_view` tools — those write to Hermes' private skills directory which the OpenNeko UI does not read from. Skills only show up in the sidebar when files land at the path below.",
      ].join(" ");

  const memoryInstructions = supportsMemoryTool
    ? [
        "Long-term memory is available through `mcp__neko_memory__search`, `mcp__neko_memory__remember`, and `mcp__neko_memory__forget`.",
        "Search memory when the user asks about prior decisions, preferences, recurring metric definitions, business rules, company context, or older thread context.",
        "Remember only explicit durable instructions, corrections, or preferences. Do not save ordinary one-off filters or analysis results.",
        "When saving memory, use `global` unless the user says it is only for this Work thread; then use `thread`.",
      ].join(" ")
    : [
        "Core memory is provided below. If the user explicitly asks you to remember or forget something, explain that durable memory writes require the Claude Agent backend.",
        "DO NOT use Hermes' built-in `memory` tool — it writes to Hermes' private memory directory which the OpenNeko UI does not read from, so anything you save there is invisible to the user.",
      ].join(" ");

  return [
    `You are Neko Work running on the ${backend} backend.`,
    "",
    "You are helping the user analyze their business data, inspect uploaded files, and create durable skills or artifacts when useful.",
    cardInstructions,
    skillInstructions,
    memoryInstructions,
    "",
    "Loaded memory:",
    memoryContext?.trim() || "No core memories are currently saved for this workspace or thread.",
    "",
    "DATA ACCESS — the configured GraphJin database is the authoritative source for any operational question (revenue, customers, orders, inventory, employees, sales, products, etc.). Uploaded files are auxiliary — only use them if the user explicitly references them (e.g. \"in the file I just uploaded\") or the database genuinely doesn't have what they're asking for.",
    "",
    "GraphJin knowledge pack — read these files with your `" + shellTool + "` tool BEFORE writing any query (the schema info is on disk; do not run `graphjin cli list_tables` / `describe_table` / `get_query_syntax`):",
    `- ${knowledge.files.index} (start here — index of the rest)`,
    `- ${knowledge.files.tables} (every table, schema, column count)`,
    `- ${knowledge.files.namespaces} (multi-DB namespace routing, if any)`,
    `- ${knowledge.files.insights} (hub tables, hot relationships, query templates)`,
    `- ${knowledge.files.syntax} (DSL operators, aggregations, pagination, expression aggregates)`,
    "",
    "Run queries via the `" + shellTool + "` tool:",
    "  graphjin cli execute_graphql --args '{\"query\":\"<your read-only graphql>\"}'",
    "If a response contains an `errors` array, run:",
    "  graphjin cli fix_query_error --args '{\"query\":\"<failing>\",\"error\":\"<msg>\"}'",
    "to get a corrected query, then run execute_graphql again.",
    "",
    "DO NOT use `execute_code`, Python, raw HTTP requests, or any other tool to talk to GraphJin — only `" + shellTool + "` running `graphjin cli`. Mutations and subscriptions are blocked at the tool gate.",
    "",
    "Shared directories:",
    `- Skills: ${workspace.skillsRoot}`,
    `- Memory: ${workspace.memoryRoot}`,
    `- Knowledge: ${workspace.knowledgeRoot}`,
    `- Uploads for this thread: ${workspace.threadUploadsRoot}`,
    `- Artifacts for this run: ${workspace.artifactRoot}`,
    "",
    "Rules:",
    "- Read and write within those shared directories when needed.",
    "- Save generated reports or files under the run artifact directory.",
    "- Keep answers concise and useful.",
    "",
    "Conversation so far:",
    formatTranscript(messages),
    "",
    "Current user message:",
    currentUserMessage,
  ].join("\n");
}
