/**
 * A2UI Protocol Types (v0.9 subset)
 *
 * Implements the message types needed for Neko:
 * - CreateSurface: initialize a rendering surface
 * - UpdateComponents: add/update components on a surface
 * - UpdateDataModel: set data that components bind to
 * - DeleteSurface: tear down a surface
 */

// --- Data Binding ---

/** A value that is either a literal or a path reference into the data model */
export type DynamicValue<T> = T | { path: string };

// --- Components ---

export interface A2UIComponent {
  id: string;
  component: string;
  [key: string]: unknown;
}

// --- Messages (Server → Client) ---

export interface CreateSurfaceMessage {
  version: "v0.9";
  createSurface: {
    surfaceId: string;
    catalogId: string;
    theme?: Record<string, unknown>;
  };
}

export interface UpdateComponentsMessage {
  version: "v0.9";
  updateComponents: {
    surfaceId: string;
    components: A2UIComponent[];
  };
}

export interface UpdateDataModelMessage {
  version: "v0.9";
  updateDataModel: {
    surfaceId: string;
    path?: string;  // JSON Pointer, defaults to "/"
    value?: unknown; // omit to delete
  };
}

export interface DeleteSurfaceMessage {
  version: "v0.9";
  deleteSurface: {
    surfaceId: string;
  };
}

export type A2UIMessage =
  | CreateSurfaceMessage
  | UpdateComponentsMessage
  | UpdateDataModelMessage
  | DeleteSurfaceMessage;

// --- Client-side Surface State ---

export interface SurfaceState {
  surfaceId: string;
  catalogId: string;
  components: Map<string, A2UIComponent>;
  dataModel: Record<string, unknown>;
  theme?: Record<string, unknown>;
}

// --- Action (Client → Server) ---

export interface A2UIAction {
  version: "v0.9";
  action: {
    name: string;
    surfaceId: string;
    sourceComponentId: string;
    timestamp: string;
    context?: Record<string, unknown>;
  };
}
