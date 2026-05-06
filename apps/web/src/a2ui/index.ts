// A2UI engine barrel export
export { CATALOG_ID, ComponentTypes } from "./catalog";
export type {
  Mood,
  ChartType,
  ChartDataPoint,
  BriefingCardProps,
  BriefingProps,
  ComponentProps,
} from "./catalog";

export type {
  A2UIMessage,
  A2UIComponent,
  SurfaceState,
  DynamicValue,
  CreateSurfaceMessage,
  UpdateComponentsMessage,
  UpdateDataModelMessage,
  A2UIAction,
} from "./types";

export {
  createSurface,
  applyMessage,
  getResolvedComponents,
  getRootComponent,
  resolveDynamic,
  resolveComponent,
} from "./surface";

export {
  registerComponent,
  renderComponent,
  renderComponentsByType,
  renderChildren,
} from "./renderer";
export type { RenderContext } from "./renderer";
