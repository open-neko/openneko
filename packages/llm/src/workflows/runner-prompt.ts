import {
  shellToolName,
  type AgentBackendId,
  type AgentWorkspace,
} from "../agent-backend";
import type { KnowledgePackContents } from "../knowledge-pack";
import {
  GRAPHJIN_DATE_RULE,
  buildDataAccessSection,
  buildMemorySection,
} from "../prompts/sections";
import type { WorkflowRecord } from "./store";

export type BuildWorkflowRunnerPromptInput = {
  workflow: WorkflowRecord;
  mode: "live" | "headless";
  memoryContext?: string;
  /** True when the backend supports in-process SDK MCP servers (Claude Agent). */
  mcpTools: boolean;
  backend: AgentBackendId;
  workspace: AgentWorkspace;
  knowledge: KnowledgePackContents;
  /** Installed plugin action kinds, so the runner uses real kinds (e.g. send_slack_dm) not generic ones. */
  pluginActions?: readonly PluginActionPromptDescriptor[];
};

const HEADLESS_TAIL = `<mode>headless</mode>

<headless_guidance>
No operator is present during this run. Make the best decision you can
from the workflow's instructions and the data available. When you
genuinely cannot proceed without operator input, ask once and the run
will pause for the operator to resume manually. Take state-changing
actions only through an approved action request — see <actions> above.
</headless_guidance>`;

const LIVE_TAIL = `<mode>live</mode>

<live_guidance>
An operator is watching this run's event stream. Use AskUserQuestion
sparingly, for genuinely ambiguous choices or irreversible decisions.
</live_guidance>`;

const MCP_OUTPUTS_BLOCK = `<outputs>
Most workflow value is non-mutating. Emit outputs liberally via
\`mcp__neko_workflow_output__emit\` — reports, findings, observations,
recommendations, briefing card proposals. Tag each with \`scope\` and
\`mood\` (\`good\`, \`watch\`, or \`act\`) so other workflows and humans
can find them.

When this workflow is an observe-and-report kind (check a signal, flag
if it moves), \`kind: "observation"\` is the right shape: emit the
observation and end. Let the work stop where the steps say it should.
</outputs>`;

// Installed plugin action kinds, surfaced to the runner the same way the
// chat path surfaces them (see work/prompt.ts). Without this the runner
// agent invents a generic `send_message`, which no adapter handles and no
// kind-scoped policy rule matches — so the action silently stalls at
// pending_approval and never sends.
export type PluginActionPromptDescriptor = {
  kind: string;
  description: string;
  default_mode?:
    | "auto"
    | "ask"
    | "deny"
    | { external?: "auto" | "ask" | "deny"; internal?: "auto" | "ask" | "deny" };
  example?: Record<string, unknown>;
};

function isDeniedEverywhere(
  defaultMode: PluginActionPromptDescriptor["default_mode"],
): boolean {
  if (defaultMode === "deny") return true;
  if (defaultMode && typeof defaultMode === "object") {
    const keys = Object.keys(defaultMode) as Array<"external" | "internal">;
    return keys.length > 0 && keys.every((k) => defaultMode[k] === "deny");
  }
  return false;
}

function activeKinds(
  pluginActions: readonly PluginActionPromptDescriptor[],
): PluginActionPromptDescriptor[] {
  return pluginActions.filter((d) => !isDeniedEverywhere(d.default_mode));
}

function installedKindsBlock(
  pluginActions: readonly PluginActionPromptDescriptor[],
): string {
  const active = activeKinds(pluginActions);
  if (active.length === 0) return "";
  const rows = active
    .map((d) => {
      const head = `  - \`${d.kind}\` — ${d.description.split("\n")[0]}`;
      return d.example
        ? `${head}\n    example payload: ${JSON.stringify(d.example)}`
        : head;
    })
    .join("\n");
  return `
Installed action kinds — when a step calls for one of these, use the
EXACT \`kind\` value below; do NOT substitute a generic kind like
\`send_message\` (the policy rules and the executing adapter both match
on the exact kind, so a generic name silently fails to route):
${rows}
`;
}

function buildMcpActionsBlock(
  pluginActions: readonly PluginActionPromptDescriptor[],
): string {
  return `<actions>
Workflows decide; actions mutate. When a step needs to change real-world
or internal state, propose it through \`mcp__neko_action__request\` and
let policy decide whether it auto-executes, queues for operator
approval, or is denied.
${installedKindsBlock(pluginActions)}
For mutations with no installed kind, use a generic kind: \`mutate_record\`,
\`open_pr\`, \`run_command\` (external) or \`memory_write\`,
\`briefing_create\`, \`schedule_workflow\` (internal).

Fill in \`risk_level\` honestly (\`low\`, \`medium\`, \`high\`,
\`critical\`) — policy uses it to route — but never repeat that value
back to the operator in prose; it's noise from their point of view.
Use the one-sentence \`summary\` to name WHAT will change and WHY in
plain language; that's what the operator may read before approving.
When a request returns \`decision: denied\`, surface the reason to the
operator and stop; re-attempting after a denial is wasted effort.
</actions>`;
}

const FENCE_OUTPUTS_BLOCK = `<outputs>
Most workflow value is non-mutating. Emit outputs liberally as fenced
JSON blocks that the runtime will execute as workflow outputs. Use
exactly this format, one block per output:

\`\`\`neko_workflow_output
{
  "kind": "observation",
  "title": "APAC revenue dipped 14% WoW",
  "body": "Revenue fell from ₹3.2L to ₹2.75L between …",
  "scope": "apac_revenue",
  "topic": "weekly_check",
  "mood": "watch"
}
\`\`\`

Allowed \`kind\` values: \`report\`, \`summary\`, \`briefing_card_proposal\`,
\`chart\`, \`table\`, \`file\`, \`message_draft\`, \`finding\`,
\`observation\`, \`recommendation\`.
Allowed \`mood\` values: \`good\`, \`watch\`, \`act\`.

Other tags: \`scope\` (e.g. "apac_revenue"), \`topic\` (more specific),
\`artifactPath\`, \`timeWindowStart\`, \`timeWindowEnd\`,
\`freshnessTtlSeconds\`. Use them when they fit; omit otherwise.

When this workflow is observe-and-report (check a signal, flag if it
moves), \`kind: "observation"\` is the right shape — emit and end.
Emit each fence exactly once. The fence body must be valid JSON.
</outputs>`;

function buildFenceActionsBlock(
  pluginActions: readonly PluginActionPromptDescriptor[],
): string {
  const exampleKind = activeKinds(pluginActions)[0]?.kind ?? "send_message";
  return `<actions>
Workflows decide; actions mutate. When a step needs to change real-world
or internal state, propose it as a fenced JSON block that the runtime
will route through policy. Use exactly this format:

\`\`\`neko_action_request
{
  "scope": "external",
  "kind": "${exampleKind}",
  "payload": { /* kind-specific fields */ },
  "risk_level": "low",
  "summary": "One plain sentence naming WHAT will change and WHY."
}
\`\`\`
${installedKindsBlock(pluginActions)}
Allowed \`scope\`: \`internal\` (memory_write, briefing_create,
schedule_workflow) or \`external\` (installed kinds above, plus generic
mutate_record, open_pr, run_command, …).
\`risk_level\` is an internal tag (\`low\`, \`medium\`, \`high\`,
\`critical\`) policy uses to route — fill it in honestly, but never
repeat the value back to the operator in prose. \`summary\` is what the
operator may read — one plain sentence naming WHAT will change and WHY.

The runtime evaluates policy. If denied, no second attempt will help;
surface the reason to the operator and stop. If approved or
auto-approved, the action is queued for execution. The fence body must
be valid JSON.
</actions>`;
}

export function buildWorkflowRunnerPrompt(
  input: BuildWorkflowRunnerPromptInput,
): string {
  const { workflow, mode, memoryContext, mcpTools, backend, workspace, knowledge } = input;
  const pluginActions = input.pluginActions ?? [];
  const shellTool = shellToolName(backend);
  const dataAccessSection = buildDataAccessSection({
    shellTool,
    workspace,
    knowledge,
    inlineKnowledge: "syntax",
  });

  const stepsBlock = workflow.steps
    .map((step, index) => `  ${index + 1}. ${step.description}`)
    .join("\n");

  const overlay = workflow.systemPromptOverlay.trim();
  const overlayBlock = overlay
    ? `<author_instructions>
The workflow's author left these rules for every run. Respect them
unless they directly conflict with safety or policy.

${overlay}
</author_instructions>

`
    : "";

  const memorySection = buildMemorySection({
    searchTool: mcpTools,
    // Workflow runner has no fence-save pipeline. When MCP isn't
    // available the agent simply can't write memories.
    saveMode: mcpTools ? "tool" : "none",
    memoryContext,
  });

  const goalBlock = workflow.goal.trim()
    ? `<goal>${workflow.goal.trim()}</goal>\n`
    : "";

  const outputsBlock = mcpTools ? MCP_OUTPUTS_BLOCK : FENCE_OUTPUTS_BLOCK;
  const actionsBlock = mcpTools
    ? buildMcpActionsBlock(pluginActions)
    : buildFenceActionsBlock(pluginActions);

  return `<role>
You are running a saved OpenNeko workflow as part of an operational loop.
Each run typically watches a signal, frames what it sees, decides what
should happen next, and emits one or more outputs. Many runs end with an
output and no further action — that's a complete shape.
</role>

<workflow>
<name>${workflow.name}</name>
<description>${workflow.description || "(none provided)"}</description>
${goalBlock}</workflow>

${overlayBlock}<steps>
Follow these in rough order. Ask the operator if anything is genuinely
ambiguous.

${stepsBlock || "  (no steps defined)"}
</steps>

<phases>
The workflow's value usually comes from one or more of these:

- Observe — gather the signals you need (sources of record, prior
  outputs, this run's inputs).
- Understand — frame what you see using memory and constraints.
- Decide — commit to a next step.
- Act — produce outputs, observations, or action requests as appropriate.
</phases>

${dataAccessSection}

<rules>
${GRAPHJIN_DATE_RULE}
</rules>

${outputsBlock}

${actionsBlock}

${memorySection}

<finishing>
After producing your output(s), send one short final assistant message
summarising what you did. Then emit exactly one fenced block estimating the
human time this run's ANALYSIS saved (exclude any actions you proposed —
those carry their own estimate):

\`\`\`neko_value
{ "minutes_saved": 12, "basis": "Checked reorder thresholds across 40 SKUs" }
\`\`\`

Estimate the minutes a competent person would spend on this by hand; be
conservative and round DOWN. Emit \`0\` when the run found nothing a person
would otherwise have acted on. Anchors (minutes): routine email 5-8 · CRM
update 3-6 · refund 12-18 · purchase order 20-30 · multi-table report 20-40 ·
summary 10-20 · single-table lookup 3-8 · found nothing 0. When you propose an
action, add \`minutes_saved\` + a short \`basis\` to it too.
</finishing>

${mode === "headless" ? HEADLESS_TAIL : LIVE_TAIL}`;
}
