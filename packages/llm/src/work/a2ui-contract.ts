import { z } from "zod";
import type { AgentSurfaceMessage } from "../agent-backend";
import { RENDER_CARDS_SCHEMA_GUIDANCE } from "./render-guidance";

export const A2UI_VERSION = "v1.0" as const;
export const A2UI_CATALOG_ID = "urn:openneko:catalog:work:v2" as const;
export const A2UI_RENDER_SERVER_NAME = "neko_ui" as const;
export const A2UI_RENDER_TOOL_NAME = "render_cards" as const;
/** Registered name for render_cards on the brokered neko MCP server. */
export const A2UI_RENDER_MCP_TOOL_NAME =
  "mcp__neko__ui_render_cards" as const;

/**
 * ACP reports tools from the multiplexed `neko` bridge as
 * `mcp_neko_<logical-server>_<tool>` in ACP notifications.
 */
export const A2UI_RENDER_ACP_TITLE =
  "mcp_neko_ui_render_cards" as const;

const a2uiComponentSchema = z
  .object({ id: z.string().min(1), component: z.string().min(1) })
  .passthrough();

function messageSchema(opts: { generated: boolean }) {
  const version = opts.generated
    ? z.literal(A2UI_VERSION)
    : z.enum(["v0.9", A2UI_VERSION]);
  const catalogId = opts.generated
    ? z.literal(A2UI_CATALOG_ID)
    : z.string().min(1);

  const createSurfaceSchema = z
    .object({
      version,
      createSurface: z
        .object({
          surfaceId: z.string().min(1),
          catalogId,
          surfaceProperties: z.record(z.string(), z.unknown()).optional(),
          sendDataModel: z.boolean().optional(),
          components: z.array(a2uiComponentSchema).min(1).optional(),
          dataModel: z.record(z.string(), z.unknown()).optional(),
        })
        .strict(),
    })
    .strict();
  const updateComponentsSchema = z
    .object({
      version,
      updateComponents: z
        .object({
          surfaceId: z.string().min(1),
          components: z.array(a2uiComponentSchema).min(1),
        })
        .strict(),
    })
    .strict();
  const updateDataModelSchema = z
    .object({
      version,
      updateDataModel: z
        .object({
          surfaceId: z.string().min(1),
          path: z.string().optional(),
          value: z.unknown().optional(),
        })
        .strict(),
    })
    .strict();
  const deleteSurfaceSchema = z
    .object({
      version,
      deleteSurface: z.object({ surfaceId: z.string().min(1) }).strict(),
    })
    .strict();

  return z.union([
    createSurfaceSchema,
    updateComponentsSchema,
    updateDataModelSchema,
    deleteSurfaceSchema,
  ]);
}

/** The sole schema for newly generated A2UI messages. */
export const generatedA2UIMessageSchema = messageSchema({ generated: true });

/** Reader compatibility for already-persisted v0.9 surfaces. */
const readableA2UIMessageSchema = messageSchema({ generated: false });

/** The sole schema accepted by the render_cards tool. */
export const renderCardsArgsSchema = z
  .object({
    messages: z
      .array(generatedA2UIMessageSchema)
      .min(1)
      .describe(RENDER_CARDS_SCHEMA_GUIDANCE),
  })
  .strict();

export const RENDER_CARDS_INPUT_SHAPE = renderCardsArgsSchema.shape;

/** JSON Schema is generated from the validator instead of maintained by hand. */
export const RENDER_CARDS_INPUT_SCHEMA = z.toJSONSchema(
  renderCardsArgsSchema,
) as Record<string, unknown>;

export type RenderCardsArgs = z.infer<typeof renderCardsArgsSchema>;

export type RenderInputValidation =
  | { success: true; messages: AgentSurfaceMessage[] }
  | {
      success: false;
      issues: Array<{ path: string; code: string; message: string }>;
    };

function generatedSurfaceGraphIssues(
  messages: RenderCardsArgs["messages"],
): Array<{ path: string; code: string; message: string }> {
  const issues: Array<{ path: string; code: string; message: string }> = [];
  messages.forEach((message, index) => {
    let location: "createSurface" | "updateComponents";
    let components: z.infer<typeof a2uiComponentSchema>[] | undefined;
    if ("createSurface" in message) {
      location = "createSurface";
      components = message.createSurface.components;
    } else if ("updateComponents" in message) {
      location = "updateComponents";
      components = message.updateComponents.components;
    } else {
      return;
    }
    if (!components) return;
    components.forEach((component, componentIndex) => {
      if (component.component === "Chart") {
        const path = `messages.${index}.${location}.components.${componentIndex}`;
        if (typeof component.title !== "string" || !component.title.trim() ||
          typeof component.valueLabel !== "string" || !component.valueLabel.trim() ||
          !["line", "bar", "area", "donut"].includes(String(component.type))) {
          issues.push({
            path,
            code: "invalid_chart_properties",
            message: "Chart requires a title, valueLabel, and type: line, bar, area, or donut.",
          });
        }
        const data = component.data;
        const boundData = data !== null && typeof data === "object" && !Array.isArray(data) &&
          "path" in data && typeof data.path === "string" && data.path.startsWith("/");
        if (!boundData && (!Array.isArray(data) || data.length < 2 || data.length > 60 ||
          data.some((point) => !point || typeof point !== "object" || Array.isArray(point) ||
            typeof point.d !== "string" || !point.d.trim() ||
            typeof point.v !== "number" || !Number.isFinite(point.v) ||
            (point.t !== undefined && (typeof point.t !== "number" || !Number.isFinite(point.t)))))) {
          issues.push({
            path: `${path}.data`,
            code: "invalid_chart_data",
            message: 'Chart needs 2–60 points [{d:"label",v:42,t?:40}] or a {path:"/series"} binding to that array.',
          });
        } else if (Array.isArray(data) && component.type === "donut" &&
          (data.length > 8 || data.some((point) => (point as { v: number }).v < 0) ||
            data.reduce((sum, point) => sum + (point as { v: number }).v, 0) <= 0)) {
          issues.push({
            path: `${path}.data`,
            code: "invalid_donut_data",
            message: "A donut needs 2–8 nonnegative category values with a positive total.",
          });
        }
      }
      if (component.component !== "Table") return;
      const path = `messages.${index}.${location}.components.${componentIndex}`;
      if (!Array.isArray(component.columns) || component.columns.length === 0 ||
        component.columns.some((column) =>
          !column || typeof column !== "object" || Array.isArray(column) ||
          typeof column.key !== "string" || !column.key ||
          typeof column.label !== "string" || !column.label
        )) {
        issues.push({
          path: `${path}.columns`,
          code: "invalid_table_columns",
          message: 'Table requires columns: [{key:"matcode",label:"Material code"}, ...].',
        });
      }
      const rows = component.rows;
      const boundRows = rows !== null && typeof rows === "object" &&
        !Array.isArray(rows) && "path" in rows && typeof rows.path === "string" &&
        rows.path.startsWith("/");
      if (!Array.isArray(rows) && !boundRows) {
        issues.push({
          path: `${path}.rows`,
          code: "invalid_table_rows",
          message: 'Table requires rows: [{matcode:"ABC", ...}] or rows: {path:"/shortfall_rows"} with an array in dataModel. dataPath is not a Table property.',
        });
      } else if (Array.isArray(rows) && rows.some((row) =>
        !row || typeof row !== "object" || Array.isArray(row)
      )) {
        issues.push({
          path: `${path}.rows`,
          code: "invalid_table_rows",
          message: "Table rows must be an array of objects keyed by the column keys.",
        });
      }
    });
    if (location !== "createSurface") return;
    const root = components.find((component) => component.id === "root");
    if (!root) {
      issues.push({
        path: `messages.${index}.createSurface.components`,
        code: "missing_root",
        message: 'A generated surface must contain a component with id "root".',
      });
      return;
    }
    if (root.component !== "Answer" || components.length === 1) return;
    if (!Array.isArray(root.children) || root.children.length === 0) {
      issues.push({
        path: `messages.${index}.createSurface.components.root.children`,
        code: "unattached_answer_components",
        message:
          "An Answer surface with additional components must attach them through root.children.",
      });
    }
  });
  return issues;
}

/** Validate the complete tool argument object. Invalid messages reject the call. */
export function validateRenderCardsInput(value: unknown): RenderInputValidation {
  const parsed = renderCardsArgsSchema.safeParse(value);
  if (parsed.success) {
    const issues = generatedSurfaceGraphIssues(parsed.data.messages);
    if (issues.length > 0) return { success: false, issues };
    return {
      success: true,
      messages: parsed.data.messages as AgentSurfaceMessage[],
    };
  }
  return {
    success: false,
    issues: parsed.error.issues.map((issue) => ({
      path: issue.path.join("."),
      code: issue.code,
      message: issue.message,
    })),
  };
}

/** Validate parsed reader input while retaining v0.9 history compatibility. */
export function coerceReadableSurfaceMessages(
  value: unknown,
): AgentSurfaceMessage[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((message) => {
    const parsed = readableA2UIMessageSchema.safeParse(message);
    return parsed.success ? [parsed.data as AgentSurfaceMessage] : [];
  });
}

/** Validate parsed model output. New render calls are v1.0 only. */
export function coerceGeneratedSurfaceMessages(
  value: unknown,
): AgentSurfaceMessage[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((message) => {
    const parsed = generatedA2UIMessageSchema.safeParse(message);
    return parsed.success ? [parsed.data as AgentSurfaceMessage] : [];
  });
}
