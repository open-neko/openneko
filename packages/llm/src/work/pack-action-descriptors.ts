import { and, db, eq, pack_action_definition } from "@neko/db";
import type { PackActionDescriptor } from "./tools";
import { getRegisteredPackActionKinds } from "../workflows/action-executor";

type PackActionDefinition = {
  kind?: unknown;
  description?: unknown;
  inputSchema?: unknown;
  example?: unknown;
  adapter?: {
    kind?: unknown;
    operations?: Record<
      string,
      {
        readPath?: unknown;
        bodyKey?: unknown;
        reversible?: unknown;
      }
    >;
  };
};

function operationContract(definition: PackActionDefinition): string {
  const operations = definition.adapter?.operations;
  if (!operations || typeof operations !== "object") return "";
  const entries = Object.entries(operations).map(([name, operation]) => {
    const path =
      typeof operation.readPath === "string"
        ? [...operation.readPath.matchAll(/\{([^}]+)\}/g)].map((match) => match[1])
        : [];
    const details = [
      path.length > 0 ? `row.path keys: ${path.join(", ")}` : null,
      typeof operation.bodyKey === "string"
        ? `row.body key: ${operation.bodyKey}`
        : null,
      typeof operation.reversible === "boolean"
        ? `reversible: ${operation.reversible ? "yes" : "no"}`
        : null,
    ].filter(Boolean);
    return `${name}${details.length > 0 ? ` (${details.join("; ")})` : ""}`;
  });
  if (entries.length === 0) return "";
  return ` Each row uses {\"entity_ref\":\"...\",\"path\":{...},\"body\":{...}}. Supported operations: ${entries.join("; ")}.`;
}

/**
 * Discover ready pack-owned actions directly from installed pack artifacts.
 * Pack actions stay separate from the plugin registry throughout discovery.
 */
export async function listPackActionDescriptors(
  orgId: string,
  options: { forHarness?: boolean } = {},
): Promise<PackActionDescriptor[]> {
  // A ready definition is not proof that the worker can execute it. Hermes
  // keeps its existing discovery behavior; Harness advertises only a native
  // registered executor or the supported declarative GraphJin API adapter.
  const registered = options.forHarness ? new Set(getRegisteredPackActionKinds()) : null;
  const rows = await db()
    .select({ definition: pack_action_definition.definition })
    .from(pack_action_definition)
    .where(
      and(
        eq(pack_action_definition.org_id, orgId),
        eq(pack_action_definition.enabled, true),
        eq(pack_action_definition.readiness, "ready"),
      ),
    );

  return rows.flatMap(({ definition }) => {
    const value = definition as PackActionDefinition;
    if (
      typeof value.kind !== "string" ||
      value.kind.length === 0 ||
      typeof value.description !== "string" ||
      value.description.length === 0
    ) {
      return [];
    }
    const adapterOperations = value.adapter?.operations;
    if (registered && !registered.has(value.kind) && !(
      value.adapter?.kind === "graphjin_api_operation" &&
      adapterOperations && typeof adapterOperations === "object" && !Array.isArray(adapterOperations) &&
      Object.keys(adapterOperations).length > 0
    )) return [];
    const schema =
      value.inputSchema && typeof value.inputSchema === "object"
        ? ` Payload schema: ${JSON.stringify(value.inputSchema)}.`
        : "";
    const operations = operationContract(value);
    const example =
      value.example &&
      typeof value.example === "object" &&
      !Array.isArray(value.example)
        ? (value.example as Record<string, unknown>)
        : undefined;
    return [
      {
        kind: value.kind,
        scope: "external" as const,
        default_mode: "ask" as const,
        description: `${value.description}${schema}${operations}`,
        ...(example ? { example } : {}),
      },
    ];
  });
}
