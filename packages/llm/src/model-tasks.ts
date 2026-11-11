export const MODEL_TASKS = [
  { task: "turn", label: "Agent turns", description: "Questions, workflows, metric runs and channel replies." },
  { task: "classify", label: "Question classifier", description: "Sorts a new question before the agent starts." },
  { task: "compact", label: "Conversation compaction", description: "Shortens a long conversation so the agent can go on." },
  { task: "bot_instructions", label: "Bot instructions", description: "Keeps a bot's standing instructions up to date." },
  { task: "bot_names", label: "Bot names", description: "Suggests a name for a new bot." },
  { task: "skill_lessons", label: "Skill lessons", description: "Proposes a skill change from a finished run." },
  { task: "research_mission", label: "Research mission", description: "Writes the company mission during setup." },
] as const;

export type ModelTask = (typeof MODEL_TASKS)[number]["task"];
export type RouteTask = ModelTask | "default";

export function isModelTask(value: unknown): value is ModelTask {
  return MODEL_TASKS.some((entry) => entry.task === value);
}

export function taskLabel(task: string | null): string {
  return MODEL_TASKS.find((entry) => entry.task === task)?.label ?? (task ? task : "Not recorded");
}
