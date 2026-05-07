import type { AgentBackendId } from "../agent-backend";
import type { WorkAgentWorkspace, WorkTranscriptMessage } from "./types";

function formatTranscript(messages: WorkTranscriptMessage[]): string {
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
  workspace: WorkAgentWorkspace;
  messages: WorkTranscriptMessage[];
  currentUserMessage: string;
  supportsCardTool: boolean;
  supportsSkillTool: boolean;
}): string {
  const { backend, workspace, messages, currentUserMessage, supportsCardTool, supportsSkillTool } =
    args;

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
    : "When the user asks you to create or update a skill, write agentskills.io-style files directly under the shared skills directory.";

  return [
    `You are Neko Work running on the ${backend} backend.`,
    "",
    "You are helping the user analyze their business data, inspect uploaded files, and create durable skills or artifacts when useful.",
    cardInstructions,
    skillInstructions,
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
    "- Use the graphjin CLI for read/query work only. Mutation, subscription, config, and server-changing commands are blocked.",
    "- Keep answers concise and useful.",
    "",
    "Conversation so far:",
    formatTranscript(messages),
    "",
    "Current user message:",
    currentUserMessage,
  ].join("\n");
}
