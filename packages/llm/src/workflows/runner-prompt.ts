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
\`mcp__neko_workflow_output__emit\` — reports, findings, observations,
recommendations, briefing card proposals. Tag each output with a
\`scope\` and \`mood\` ('good' / 'watch' / 'act') so other workflows
and humans can find it.

If this workflow is an observe-and-report kind (watch a signal and
flag it), \`kind: "observation"\` is the right shape — emit the
observation and end. Don't manufacture follow-up steps that aren't
in the workflow's instructions.

ACTIONS:
Workflows decide; actions mutate. If you need to change real-world or
internal state, NEVER do it directly — call
\`mcp__neko_action__request\` to propose the action. Policy decides
whether it auto-executes, queues for operator approval, or is denied.

Use action requests for:
  - external mutations (send_message, mutate_record, open_pr, run_command, ...)
  - internal state changes that need gating at scale (memory_write,
    briefing_create, schedule_workflow, ...)

Provide an honest \`risk_level\` and a 1-sentence \`summary\` that names
WHAT will change and WHY — the operator may read it before approving.

If the action request returns \`decision: denied\`, surface the reason to
the operator. Do not retry. Do not work around the denial.

LONG-TERM MEMORY:
${memoryBlock}

FINISHING:
After producing your output(s), send one short final assistant message
summarising what you did. Stop there.${mode === "headless" ? HEADLESS_TAIL : LIVE_TAIL}`;
}
