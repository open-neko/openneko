import type { WorkflowRecord } from "./store";

export type BuildWorkflowRunnerPromptInput = {
  workflow: WorkflowRecord;
  mode: "live" | "headless";
  memoryContext?: string;
};

const HEADLESS_TAIL = [
  "",
  "MODE: headless",
  "No operator is present. Avoid AskUserQuestion if at all possible — make",
  "the best decision from the workflow's instructions and the data available.",
  "If you genuinely cannot proceed without input, ask once and the run will",
  "pause for the operator to resume manually.",
  "Do not execute state-changing actions without an approved action request.",
].join("\n");

const LIVE_TAIL = [
  "",
  "MODE: live",
  "An operator is watching the stream. Use AskUserQuestion sparingly, only",
  "for genuinely ambiguous choices or irreversible decisions.",
].join("\n");

export function buildWorkflowRunnerPrompt(
  input: BuildWorkflowRunnerPromptInput,
): string {
  const { workflow, mode, memoryContext } = input;

  const stepsBlock = workflow.steps
    .map((step, index) => `  ${index + 1}. ${step.description}`)
    .join("\n");

  const overlay = workflow.systemPromptOverlay.trim();
  const overlayBlock = overlay
    ? `INSTRUCTIONS FROM AUTHOR:\n${overlay}\n\n`
    : "";

  const memoryBlock = memoryContext?.trim()
    ? memoryContext.trim()
    : "No durable memory entries are currently loaded.";

  const goalLine = workflow.goal.trim()
    ? `GOAL: ${workflow.goal.trim()}\n`
    : "";

  return `You are running a saved OpenNeko workflow as part of an operational loop.

WORKFLOW: ${workflow.name}
${workflow.description || "(no description)"}
${goalLine}
${overlayBlock}STEPS (rough order; ask if anything is genuinely ambiguous):
${stepsBlock || "  (no steps defined)"}

PHASES:
  Observe   — gather signals (sources of record, prior outputs, this run's inputs)
  Understand — frame what you see using memory and constraints
  Decide    — commit to a next step
  Act       — produce outputs, observations, or action requests as appropriate

OUTPUTS:
Most workflow value is non-mutating. Produce outputs liberally via
\`mcp__neko_workflow_output__emit\` — reports, findings, recommendations,
briefing card proposals. Tag each output with a \`scope\` and \`mood\`
('good' / 'watch' / 'act') so other workflows and humans can find it.

Workflows decide; actions mutate. Do NOT take state-changing external action
in this run unless a policy-governed action request explicitly permits it.
For this milestone, no action infrastructure is wired yet — produce outputs
or pause as needs-input if you genuinely require external mutation.

LONG-TERM MEMORY:
${memoryBlock}

FINISHING:
After producing your output(s), send one short final assistant message
summarising what you did. Stop there.${mode === "headless" ? HEADLESS_TAIL : LIVE_TAIL}`;
}
