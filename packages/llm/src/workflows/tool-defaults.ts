export const WORKFLOW_BUILDER_ALLOWED_TOOLS = [
  "Read",
  "Glob",
  "Grep",
  "WebFetch",
  "WebSearch",
  "AskUserQuestion",
  "mcp__neko_workflow_builder__*",
] as const;

export const WORKFLOW_RUNNER_DEFAULT_ALLOWED_TOOLS = [
  "Bash",
  "Read",
  "Write",
  "Edit",
  "Glob",
  "Grep",
  "WebFetch",
  "WebSearch",
  "AskUserQuestion",
  "Skill",
  "mcp__neko_ui__*",
  "mcp__neko_memory__*",
  "mcp__neko_workflow_output__*",
] as const;

export const WORKFLOW_FIXED_DENY = [
  "Monitor",
  "Task",
  "TaskCreate",
  "TaskUpdate",
  "TaskList",
  "TaskGet",
  "TaskOutput",
  "TaskStop",
  "ToolSearch",
  "NotebookEdit",
] as const;

export function toolMatches(name: string, pattern: string): boolean {
  if (pattern.endsWith("*")) return name.startsWith(pattern.slice(0, -1));
  return name === pattern;
}

export function buildAllowDenyGate(
  allowed: readonly string[],
  denied: readonly string[],
) {
  return async (
    toolName: string,
    input: Record<string, unknown>,
  ): Promise<Record<string, unknown>> => {
    if (denied.some((p) => toolMatches(toolName, p))) {
      return {
        behavior: "deny",
        message: `Tool "${toolName}" is denied for this run.`,
      };
    }
    if (!allowed.some((p) => toolMatches(toolName, p))) {
      return {
        behavior: "deny",
        message: `Tool "${toolName}" is not in the allowlist. Allowed: ${allowed.join(", ")}.`,
      };
    }
    return { behavior: "allow", updatedInput: input };
  };
}
