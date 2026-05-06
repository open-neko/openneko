/**
 * A2UI Surface Engine
 *
 * Processes A2UI messages and maintains surface state.
 * The surface holds the component tree and data model,
 * resolving dynamic values (path references) against the data model.
 */

import type {
  A2UIMessage,
  A2UIComponent,
  SurfaceState,
  DynamicValue,
} from "./types";

/** Resolve a JSON Pointer path against an object */
function resolvePointer(obj: Record<string, unknown>, pointer: string): unknown {
  if (!pointer || pointer === "/") return obj;
  const parts = pointer.replace(/^\//, "").split("/");
  let current: unknown = obj;
  for (const part of parts) {
    if (current == null || typeof current !== "object") return undefined;
    current = (current as Record<string, unknown>)[part];
  }
  return current;
}

/** Set a value at a JSON Pointer path */
function setPointer(obj: Record<string, unknown>, pointer: string, value: unknown): Record<string, unknown> {
  const result = structuredClone(obj);
  if (!pointer || pointer === "/") {
    return value as Record<string, unknown>;
  }
  const parts = pointer.replace(/^\//, "").split("/");
  let current: Record<string, unknown> = result;
  for (let i = 0; i < parts.length - 1; i++) {
    if (current[parts[i]] == null || typeof current[parts[i]] !== "object") {
      current[parts[i]] = {};
    }
    current = current[parts[i]] as Record<string, unknown>;
  }
  if (value === undefined) {
    delete current[parts[parts.length - 1]];
  } else {
    current[parts[parts.length - 1]] = value;
  }
  return result;
}

/** Resolve a DynamicValue — if it has a path, look it up in the data model */
export function resolveDynamic<T>(val: DynamicValue<T>, dataModel: Record<string, unknown>): T {
  if (val != null && typeof val === "object" && "path" in val) {
    return resolvePointer(dataModel, (val as { path: string }).path) as T;
  }
  return val as T;
}

/** Resolve all dynamic values in a component's properties */
export function resolveComponent(
  component: A2UIComponent,
  dataModel: Record<string, unknown>
): A2UIComponent {
  const resolved: A2UIComponent = { id: component.id, component: component.component };
  for (const [key, value] of Object.entries(component)) {
    if (key === "id" || key === "component") continue;
    resolved[key] = resolveDynamic(value as DynamicValue<unknown>, dataModel);
  }
  return resolved;
}

/** Create a new empty surface */
export function createSurface(surfaceId: string, catalogId: string, theme?: Record<string, unknown>): SurfaceState {
  return {
    surfaceId,
    catalogId,
    components: new Map(),
    dataModel: {},
    theme,
  };
}

/** Apply an A2UI message to surface state, returning updated state (immutable) */
export function applyMessage(
  surfaces: Map<string, SurfaceState>,
  message: A2UIMessage
): Map<string, SurfaceState> {
  const next = new Map(surfaces);

  if ("createSurface" in message) {
    const { surfaceId, catalogId, theme } = message.createSurface;
    next.set(surfaceId, createSurface(surfaceId, catalogId, theme));
  }

  if ("updateComponents" in message) {
    const { surfaceId, components } = message.updateComponents;
    const surface = next.get(surfaceId);
    if (!surface) return next;
    const updated = { ...surface, components: new Map(surface.components) };
    for (const comp of components) {
      updated.components.set(comp.id, comp);
    }
    next.set(surfaceId, updated);
  }

  if ("updateDataModel" in message) {
    const { surfaceId, path, value } = message.updateDataModel;
    const surface = next.get(surfaceId);
    if (!surface) return next;
    next.set(surfaceId, {
      ...surface,
      dataModel: setPointer(surface.dataModel, path ?? "/", value),
    });
  }

  if ("deleteSurface" in message) {
    next.delete(message.deleteSurface.surfaceId);
  }

  return next;
}

/** Get all components for a surface, with dynamic values resolved */
export function getResolvedComponents(surface: SurfaceState): A2UIComponent[] {
  return Array.from(surface.components.values()).map(
    (c) => resolveComponent(c, surface.dataModel)
  );
}

/** Get the root component of a surface */
export function getRootComponent(surface: SurfaceState): A2UIComponent | undefined {
  return surface.components.get("root");
}
