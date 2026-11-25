export type ModelPrice = { inputUsdPer1M: number; outputUsdPer1M: number };

export type SettingsField = {
  key: string;
  label: string;
  kind: "text" | "secret" | "url";
  required?: boolean;
  placeholder?: string;
  help?: string;
};

export type PublicModel = {
  id: string;
  name: string;
  provider: string;
  model: string;
  enabled: boolean;
  config: Record<string, unknown>;
  secretStatus: Record<string, string>;
  price: ModelPrice | null;
  decision: boolean;
};

export type PublicRoute = {
  task: string;
  label: string;
  description: string;
  modelId: string | null;
  fallbackModelId: string | null;
};

export type ModelSettings = {
  defaultModel: { provider: string; model: string; source: string };
  models: PublicModel[];
  routes: PublicRoute[];
  catalog: {
    providers: ReadonlyArray<{ value: string; label: string; description: string }>;
    defaults: Record<string, string>;
    fields: Record<string, SettingsField[]>;
  };
};

export const DECIDE_TASK = "decide";

export const routeModels = (route: PublicRoute, models: PublicModel[]) =>
  route.task === DECIDE_TASK ? models : models.filter((model) => !model.decision);
