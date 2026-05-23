import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import type { AgentEvent } from "../agent-backend";
import { graphjinQuery } from "../graphjin/client";
import { subscriptionSavedCard } from "./builder-cards";
import { detectMutationLoop } from "./cycle-detection";
import {
  createSubscription,
  getDataSourceForOrg,
  getWorkflow,
  getWorkflowByOrgName,
  listSubscriptionsByWorkflow,
  type SubscriptionRecord,
} from "./store";
import {
  buildSourceChangeDryRunQuery,
  parseSourceChangeFilter,
} from "./subscription-query";

export type SubscriptionBuilderContext = {
  orgId: string;
  emit?: (event: AgentEvent) => Promise<void> | void;
};

const SOURCE_CHANGE_FILTER_SCHEMA = z.object({
  table: z.string().min(1).max(120),
  where: z.record(z.string(), z.unknown()).optional(),
  select: z.array(z.string().min(1).max(120)).optional(),
  primary_key: z.array(z.string().min(1).max(120)).min(1).max(8),
  version_column: z.string().min(1).max(120).optional(),
});

const CREATE_SUBSCRIPTION_INPUT = {
  workflow_name: z
    .string()
    .min(1)
    .max(120)
    .describe(
      "Exact name of the responder workflow (the one that fires when the trigger matches). Use list_workflows first if you don't know it.",
    ),
  source_kind: z
    .enum(["source_change"])
    .default("source_change")
    .describe(
      "What this subscription watches. source_change = a row in the operator's data source matching `filter` changes. workflow_output flows are not configured via this tool; use list_workflows + subscribe via the workflow detail page.",
    ),
  filter: SOURCE_CHANGE_FILTER_SCHEMA.describe(
    "GraphJin where-clause + table + primary_key. Use describe_table from the GraphJin MCP to discover columns. primary_key is REQUIRED (composite keys allowed) and drives delivery idempotency.",
  ),
  enabled: z.boolean().optional(),
  idempotency_key_template: z
    .string()
    .max(200)
    .optional()
    .describe(
      "Custom singleton-key template. Tokens: {subscription_id}, {primary_key}, {source_version}. Set when the responder workflow mutates the watched table so the same row can't re-trigger itself within an hour.",
    ),
  acknowledge_mutation_loop: z
    .boolean()
    .optional()
    .describe(
      "Set to true to bypass the save-time check that warns when the responder's text mentions the watched table + a mutation verb. Only set after confirming with the operator that the loop is intended (or idempotency_key_template is set).",
    ),
};

const DRY_RUN_INPUT = {
  filter: SOURCE_CHANGE_FILTER_SCHEMA,
  limit: z.number().int().min(1).max(50).optional(),
};

const LIST_SUBSCRIPTIONS_INPUT = {
  workflow_name: z.string().min(1).max(120).optional(),
};

export function buildSubscriptionBuilderServer(
  ctx: SubscriptionBuilderContext,
) {
  const createSubscriptionTool = tool(
    "create_subscription",
    [
      "Wire a workflow to trigger when a row in the operator's data source",
      "matches a filter (the IFTTT 'if this' side). Before calling, use the",
      "GraphJin MCP to introspect the schema: `list_tables`, `describe_table`",
      "to confirm column names and the primary_key. Use `dry_run_subscription`",
      "to preview which rows match BEFORE saving — most failures come from",
      "filters that match more (or fewer) rows than the operator expected.",
      "Errors return a structured `code` so you can retry with a fix.",
    ].join(" "),
    CREATE_SUBSCRIPTION_INPUT,
    async (args) => {
      const workflow = await getWorkflowByOrgName(ctx.orgId, args.workflow_name);
      if (!workflow) {
        return toolError({
          code: "workflow_not_found",
          message: `no workflow named "${args.workflow_name}" in this org. Use list_workflows to find the right name.`,
        });
      }

      const parsed = parseSourceChangeFilter(args.filter);
      if (!parsed) {
        return toolError({
          code: "invalid_filter",
          message:
            "filter shape rejected — check table/primary_key are identifiers and primary_key is non-empty.",
        });
      }

      const dataSource = await getDataSourceForOrg(ctx.orgId);
      if (!dataSource) {
        return toolError({
          code: "no_data_source",
          message:
            "no data_source configured for this org. Source_change subscriptions need a configured data source.",
        });
      }

      if (!args.acknowledge_mutation_loop) {
        const loop = detectMutationLoop({ filter: parsed, workflow });
        const hasIdempotency =
          typeof args.idempotency_key_template === "string" &&
          args.idempotency_key_template.length > 0;
        if (loop.loops && !hasIdempotency) {
          return toolError({
            code: "mutation_loop",
            message: loop.reason,
            mutationKeyword: loop.mutationKeyword,
            hint: "Confirm with the operator. If the loop is intended, set idempotency_key_template (e.g. 'reorder-{primary_key}') or pass acknowledge_mutation_loop: true.",
          });
        }
      }

      const sub = await createSubscription({
        orgId: ctx.orgId,
        workflowId: workflow.id,
        sourceKind: "source_change",
        filter: args.filter as Record<string, unknown>,
        enabled: args.enabled ?? true,
        idempotencyKeyTemplate: args.idempotency_key_template ?? null,
      });

      if (ctx.emit) {
        await ctx.emit({
          type: "surface",
          messages: subscriptionSavedCard({
            subscription: sub,
            workflowName: workflow.name,
          }),
        });
      }

      return toolOk({
        action: "created",
        subscriptionId: sub.id,
        workflowId: workflow.id,
        workflowName: workflow.name,
        table: parsed.table,
        primaryKey: parsed.primary_key,
      });
    },
  );

  const dryRunSubscriptionTool = tool(
    "dry_run_subscription",
    [
      "Compile a proposed source_change filter and run it as a one-shot read",
      "against the data source, returning up to `limit` matching rows. Use",
      "this BEFORE create_subscription to validate the filter against real",
      "data — checks both that the filter is syntactically valid and that",
      "the matches are what the operator expected.",
    ].join(" "),
    DRY_RUN_INPUT,
    async (args) => {
      const parsed = parseSourceChangeFilter(args.filter);
      if (!parsed) {
        return toolError({
          code: "invalid_filter",
          message: "filter rejected at compile time.",
        });
      }
      const dataSource = await getDataSourceForOrg(ctx.orgId);
      if (!dataSource) {
        return toolError({
          code: "no_data_source",
          message: "no data_source configured for this org.",
        });
      }
      const limit = args.limit ?? 5;
      const payload = buildSourceChangeDryRunQuery(args.filter, limit);
      if (!payload) {
        return toolError({
          code: "compile_failed",
          message: "filter passed validation but failed to compile.",
        });
      }
      let result;
      try {
        result = await graphjinQuery({
          baseUrl: dataSource.graphqlUrl,
          query: payload.query,
          variables: payload.variables,
        });
      } catch (err) {
        return toolError({
          code: "graphjin_error",
          message: err instanceof Error ? err.message : String(err),
          compiledQuery: payload.query,
        });
      }
      if (result.errors && result.errors.length > 0) {
        return toolError({
          code: "graphjin_errors",
          message: "graphjin returned errors — usually means the table/column doesn't exist or the where clause is malformed.",
          compiledQuery: payload.query,
          graphjinErrors: result.errors,
        });
      }
      const data = (result.data ?? {}) as Record<string, unknown>;
      const raw = data[parsed.table];
      const rows: unknown[] = Array.isArray(raw) ? raw : [];
      return toolOk({
        table: parsed.table,
        rowCount: rows.length,
        rows,
        compiledQuery: payload.query,
      });
    },
  );

  const listSubscriptionsTool = tool(
    "list_subscriptions",
    [
      "List existing subscriptions for a workflow, so you can answer questions",
      "like 'what's triggering this' or check before creating a duplicate.",
      "If workflow_name is omitted, returns nothing (the org-wide listing",
      "lives in the /workflows UI, not here, to keep token use bounded).",
    ].join(" "),
    LIST_SUBSCRIPTIONS_INPUT,
    async (args) => {
      if (!args.workflow_name) {
        return toolOk({ subscriptions: [] });
      }
      const workflow = await getWorkflowByOrgName(
        ctx.orgId,
        args.workflow_name,
      );
      if (!workflow) {
        return toolError({
          code: "workflow_not_found",
          message: `no workflow named "${args.workflow_name}".`,
        });
      }
      const subs = await listSubscriptionsByWorkflow(ctx.orgId, workflow.id);
      return toolOk({
        workflowId: workflow.id,
        workflowName: workflow.name,
        subscriptions: subs.map(summarizeSubscription),
      });
    },
  );

  return createSdkMcpServer({
    name: "neko_subscription_builder",
    version: "1.0.0",
    tools: [
      createSubscriptionTool,
      dryRunSubscriptionTool,
      listSubscriptionsTool,
    ],
  });
}

function summarizeSubscription(s: SubscriptionRecord) {
  return {
    id: s.id,
    sourceKind: s.sourceKind,
    filter: s.filter,
    enabled: s.enabled,
    idempotencyKeyTemplate: s.idempotencyKeyTemplate,
    createdAt: s.createdAt.toISOString(),
  };
}

function toolOk(payload: Record<string, unknown>) {
  return {
    content: [
      {
        type: "text" as const,
        text: JSON.stringify({ ok: true, ...payload }),
      },
    ],
  };
}

function toolError(payload: { code: string; message: string } & Record<string, unknown>) {
  return {
    content: [
      {
        type: "text" as const,
        text: JSON.stringify({ ok: false, ...payload }),
      },
    ],
    isError: true,
  };
}
