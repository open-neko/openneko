import type { AgentSurfaceMessage } from "../agent-backend";
import type { ActionPolicyRecord } from "./action-store";
import type { WorkflowRecord } from "./store";

function briefingCard(args: {
  surfaceId: string;
  greeting: string;
  subtitle: string;
  body: string;
}): AgentSurfaceMessage[] {
  return [
    {
      version: "v0.9",
      createSurface: {
        surfaceId: args.surfaceId,
        catalogId: "urn:app:catalog:briefing:v1",
      },
    },
    {
      version: "v0.9",
      updateComponents: {
        surfaceId: args.surfaceId,
        components: [
          {
            id: "root",
            component: "Briefing",
            greeting: args.greeting,
            subtitle: args.subtitle,
            role: "operator",
            children: ["body"],
          },
          { id: "body", component: "Markdown", text: args.body },
        ],
      },
    },
  ];
}

export function workflowSavedCard(args: {
  workflow: WorkflowRecord;
  action: "created" | "updated";
}): AgentSurfaceMessage[] {
  const verb = args.action === "created" ? "Created" : "Updated";
  const cron = args.workflow.cron
    ? ` (cron \`${args.workflow.cron}\` ${args.workflow.cronTimezone})`
    : "";
  return briefingCard({
    surfaceId: `workflow-save-${args.workflow.id}`,
    greeting: `${verb} workflow`,
    subtitle: args.workflow.name,
    body: [
      `**${args.workflow.name}** — ${args.workflow.steps.length} step(s)${cron}.`,
      "",
      args.workflow.description || "_No description._",
      "",
      `[Open detail](/work/workflows/${args.workflow.id})`,
    ].join("\n"),
  });
}

export function policySavedCard(args: {
  policy: ActionPolicyRecord;
  action: "created" | "updated";
}): AgentSurfaceMessage[] {
  const verb = args.action === "created" ? "Created" : "Updated";
  const scopes = args.policy.appliesToScopes.join(", ") || "—";
  const kinds =
    args.policy.appliesToKinds.length > 0
      ? args.policy.appliesToKinds.map((k) => `\`${k}\``).join(", ")
      : "_(all kinds)_";
  return briefingCard({
    surfaceId: `policy-save-${args.policy.id}`,
    greeting: `${verb} rule`,
    subtitle: args.policy.name,
    body: [
      `**Mode:** \`${args.policy.mode}\` • **Scopes:** ${scopes} • **Priority:** ${args.policy.priority}`,
      "",
      `**Kinds:** ${kinds}`,
      "",
      args.policy.description || "_No description._",
      "",
      `[Open detail](/settings/rules/${args.policy.id})`,
    ].join("\n"),
  });
}
