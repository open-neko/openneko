import type { WorkflowRecord } from "./store";

export type BuildWorkflowRunnerPromptInput = {
  workflow: WorkflowRecord;
  mode: "live" | "headless";
  memoryContext?: string;
};

const HEADLESS_TAIL = `## Mode: headless

No operator is present during this run. Make the best decision you can
from the workflow's instructions and the data available. When you
genuinely cannot proceed without operator input, ask once and the run
will pause for the operator to resume manually. Take state-changing
actions only through an approved action request — see Actions above.`;

const LIVE_TAIL = `## Mode: live

An operator is watching this run's event stream. Use AskUserQuestion
sparingly, for genuinely ambiguous choices or irreversible decisions.`;

export function buildWorkflowRunnerPrompt(
  input: BuildWorkflowRunnerPromptInput,
): string {
  const { workflow, mode, memoryContext } = input;

  const stepsBlock = workflow.steps
    .map((step, index) => `  ${index + 1}. ${step.description}`)
    .join("\n");

  const overlay = workflow.systemPromptOverlay.trim();

  // Author-supplied content is fenced so the runner can tell it apart
  // from the system instructions. Keep the fence even when content is
  // short — the structure helps every backend, not just Claude.
  const overlayBlock = overlay
    ? `## Author instructions

The workflow's author left these rules for every run. Respect them
unless they directly conflict with safety or policy.

\`\`\`
${overlay}
\`\`\`

`
    : "";

  const memoryBlock = memoryContext?.trim()
    ? memoryContext.trim()
    : "_No durable memory entries are currently loaded._";

  const goalLine = workflow.goal.trim()
    ? `**Goal:** ${workflow.goal.trim()}\n`
    : "";

  return `## Role

You are running a saved OpenNeko workflow as part of an operational loop.
Each run typically watches a signal, frames what it sees, decides what
should happen next, and emits one or more outputs. Many runs end with an
output and no further action — that's a complete shape.

## Workflow

**Name:** ${workflow.name}
**Description:** ${workflow.description || "(none provided)"}
${goalLine}
${overlayBlock}## Steps

Follow these in rough order. Ask the operator if anything is genuinely
ambiguous.

${stepsBlock || "  (no steps defined)"}

## Phases

The workflow's value usually comes from one or more of these:

- **Observe** — gather the signals you need (sources of record, prior
  outputs, this run's inputs).
- **Understand** — frame what you see using memory and constraints.
- **Decide** — commit to a next step.
- **Act** — produce outputs, observations, or action requests as
  appropriate.

## Outputs

Most workflow value is non-mutating. Emit outputs liberally via
\`mcp__neko_workflow_output__emit\` — reports, findings, observations,
recommendations, briefing card proposals. Tag each with \`scope\` and
\`mood\` (\`good\`, \`watch\`, or \`act\`) so other workflows and humans
can find them.

When this workflow is an observe-and-report kind (watch a signal, flag
if it moves), \`kind: "observation"\` is the right shape: emit the
observation and end. Let the work stop where the steps say it should.

## Actions

Workflows decide; actions mutate. When a step needs to change real-world
or internal state, propose it through \`mcp__neko_action__request\` and
let policy decide whether it auto-executes, queues for operator
approval, or is denied.

The action tool covers:

- **External mutations:** \`send_message\`, \`mutate_record\`,
  \`open_pr\`, \`run_command\`, etc.
- **Internal state changes** that need gating at scale:
  \`memory_write\`, \`briefing_create\`, \`schedule_workflow\`, etc.

Provide an honest \`risk_level\` and a one-sentence \`summary\` naming
WHAT will change and WHY — the operator may read it before approving.
When a request returns \`decision: denied\`, surface the reason to the
operator and stop; re-attempting after a denial is wasted effort.

## Long-term memory

${memoryBlock}

## Finishing

After producing your output(s), send one short final assistant message
summarising what you did, then end the run.

${mode === "headless" ? HEADLESS_TAIL : LIVE_TAIL}`;
}
