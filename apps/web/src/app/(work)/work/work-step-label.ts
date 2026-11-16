import type { ToolItem } from "./work-types";

const FAILED_STEP = "That step failed; trying another way";

export function toolFailed(tool: ToolItem): boolean {
  return Boolean(tool.end?.error) || tool.end?.failed === true;
}

/** A stored run from before the server sent labels has only the tool id. */
function nameAsWords(name: string): string {
  const words = name
    .replace(/^mcp_{1,2}/, "")
    .replace(/^neko_{1,2}/, "")
    .replace(/_+/g, " ")
    .trim();
  return words ? words[0].toUpperCase() + words.slice(1) : "Working on the next step";
}

/** The one plain line a tool call shows: what it does, or why it failed. */
export function stepLabel(tool: ToolItem): string {
  if (toolFailed(tool)) return tool.end?.label?.trim() || FAILED_STEP;
  return tool.label?.trim() || nameAsWords(tool.name);
}

/** The step a group of calls shows: the call that runs now, else the newest. */
export function currentTool(tools: ToolItem[]): ToolItem | undefined {
  for (let i = tools.length - 1; i >= 0; i--) {
    if (!tools[i].end) return tools[i];
  }
  return tools[tools.length - 1];
}
