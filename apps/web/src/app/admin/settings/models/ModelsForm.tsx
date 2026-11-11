"use client";

import { useState } from "react";
import { toast } from "sonner";
import type { ModelSettings, PublicModel, PublicRoute } from "@neko/llm/model-routes";
import { AdminError } from "@/components/admin/AdminError";
import { adminApi } from "@/components/admin/admin-api";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, Input, NativeSelect } from "@/components/ui/field";

type Draft = {
  id?: string;
  name: string;
  provider: string;
  model: string;
  enabled: boolean;
  config: Record<string, string>;
  secrets: Record<string, string>;
  secretStatus: Record<string, string>;
  inputPrice: string;
  outputPrice: string;
};

function emptyDraft(settings: ModelSettings): Draft {
  const provider = settings.catalog.providers[0]?.value ?? "openai";
  return {
    name: "",
    provider,
    model: settings.catalog.defaults[provider] ?? "",
    enabled: true,
    config: {},
    secrets: {},
    secretStatus: {},
    inputPrice: "",
    outputPrice: "",
  };
}

function draftOf(model: PublicModel): Draft {
  return {
    id: model.id,
    name: model.name,
    provider: model.provider,
    model: model.model,
    enabled: model.enabled,
    config: Object.fromEntries(Object.entries(model.config).map(([key, value]) => [key, String(value ?? "")])),
    secrets: {},
    secretStatus: model.secretStatus,
    inputPrice: model.price ? String(model.price.inputUsdPer1M) : "",
    outputPrice: model.price ? String(model.price.outputUsdPer1M) : "",
  };
}

const priceText = (model: PublicModel) =>
  model.price ? `$${model.price.inputUsdPer1M} in · $${model.price.outputUsdPer1M} out per 1M tokens` : "Catalog price";

export default function ModelsForm({ initial }: { initial: ModelSettings }) {
  const [settings, setSettings] = useState(initial);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function saveDraft(event: React.FormEvent) {
    event.preventDefault();
    if (!draft) return;
    setBusy(true);
    setError(null);
    const priced = draft.inputPrice.trim() !== "" || draft.outputPrice.trim() !== "";
    const body = {
      name: draft.name.trim(),
      provider: draft.provider,
      model: draft.model.trim(),
      enabled: draft.enabled,
      config: draft.config,
      secrets: Object.fromEntries(Object.entries(draft.secrets).filter(([, value]) => value.trim())),
      price: priced ? { inputUsdPer1M: Number(draft.inputPrice), outputUsdPer1M: Number(draft.outputPrice) } : null,
    };
    const result = draft.id
      ? await adminApi<{ settings: ModelSettings }>(`/api/v1/admin/models/${draft.id}`, "PUT", body)
      : await adminApi<{ settings: ModelSettings }>("/api/v1/admin/models", "POST", body);
    setBusy(false);
    if (!result.ok) return setError(result.error);
    setSettings(result.body.settings);
    setDraft(null);
    toast.success(`Model ${body.name} saved.`);
  }

  async function remove(model: PublicModel) {
    setBusy(true);
    setError(null);
    const result = await adminApi<ModelSettings>(`/api/v1/admin/models/${model.id}`, "DELETE");
    setBusy(false);
    if (!result.ok) return setError(result.error);
    setSettings(result.body);
    toast.success(`Model ${model.name} removed.`);
  }

  async function saveRoute(route: PublicRoute, change: Partial<Pick<PublicRoute, "modelId" | "fallbackModelId">>) {
    setBusy(true);
    setError(null);
    const next = { ...route, ...change };
    const result = await adminApi<ModelSettings>("/api/v1/admin/model-routes", "PUT", {
      task: route.task,
      modelId: next.modelId,
      fallbackModelId: next.fallbackModelId,
    });
    setBusy(false);
    if (!result.ok) return setError(result.error);
    setSettings(result.body);
    toast.success(`Route for ${route.label} saved.`);
  }

  const defaultLabel = settings.defaultModel.model
    ? `Default model (${settings.defaultModel.provider}/${settings.defaultModel.model})`
    : "Default model";

  return (
    <div className="root">
      <AppHeader back={{ href: "/admin/settings", label: "Settings" }} />
      <PageHeading
        title="Models and task routes"
        description="Add models beside the default model, choose a model for each task, and name a fallback model for provider outages."
      />
      <AdminError message={error} />

      <section className="settings-card flex flex-col gap-4" aria-label="Models">
        <div className="flex items-start justify-between gap-3 max-[720px]:flex-col">
          <div>
            <h2 className="settings-card-title">Models</h2>
            <p className="settings-card-copy">
              The default model is set in Model and provider. A price here replaces the catalog price in the spend meter.
            </p>
          </div>
          {draft ? null : (
            <Button type="button" onClick={() => setDraft(emptyDraft(settings))} disabled={busy}>
              Add a model
            </Button>
          )}
        </div>
        {settings.models.length === 0 ? (
          <p className="text-ui-body-sm text-text3">No other models yet. Every task uses the default model.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-border">
            {settings.models.map((model) => (
              <li key={model.id} className="flex items-start justify-between gap-3 py-3 max-[720px]:flex-col">
                <div className="flex min-w-0 flex-col gap-1">
                  <span className="font-medium">
                    {model.name}
                    {model.enabled ? null : <span className="ml-2 text-ui-caption text-text3">off</span>}
                  </span>
                  <span className="text-ui-body-sm break-all text-text2">
                    {model.provider}/{model.model}
                  </span>
                  <span className="text-ui-caption text-text3">{priceText(model)}</span>
                </div>
                <div className="flex gap-2">
                  <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={() => setDraft(draftOf(model))}>
                    Edit
                  </Button>
                  <Button type="button" size="sm" variant="danger" disabled={busy} onClick={() => void remove(model)}>
                    Remove
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        {draft ? (
          <ModelEditor draft={draft} settings={settings} busy={busy} onChange={setDraft} onCancel={() => setDraft(null)} onSubmit={saveDraft} />
        ) : null}
      </section>

      <section className="settings-card mt-6 flex flex-col gap-4" aria-label="Task routes">
        <div>
          <h2 className="settings-card-title">Task routes</h2>
          <p className="settings-card-copy">
            A task with no model of its own follows the Default route. The fallback takes a call only when the provider is
            down, overloaded or times out; a refused key or a bad request stays an error.
          </p>
        </div>
        <ul className="flex flex-col divide-y divide-border">
          {settings.routes.map((route) => (
            <li key={route.task} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)] gap-3 py-3 max-[720px]:grid-cols-1">
              <div className="flex flex-col gap-1">
                <span className="font-medium">{route.label}</span>
                <span className="text-ui-caption text-text3">{route.description}</span>
              </div>
              <Field label="Model" htmlFor={`route-${route.task}-model`}>
                <NativeSelect
                  id={`route-${route.task}-model`}
                  value={route.modelId ?? ""}
                  disabled={busy || route.task === "default"}
                  onChange={(e) => void saveRoute(route, { modelId: e.target.value || null })}
                >
                  <option value="">{route.task === "default" ? defaultLabel : "Follow the Default route"}</option>
                  {settings.models.map((model) => (
                    <option key={model.id} value={model.id}>
                      {model.name}
                    </option>
                  ))}
                </NativeSelect>
              </Field>
              <Field label="Fallback" htmlFor={`route-${route.task}-fallback`}>
                <NativeSelect
                  id={`route-${route.task}-fallback`}
                  value={route.fallbackModelId ?? ""}
                  disabled={busy}
                  onChange={(e) => void saveRoute(route, { fallbackModelId: e.target.value || null })}
                >
                  <option value="">No fallback</option>
                  {settings.models
                    .filter((model) => model.id !== route.modelId)
                    .map((model) => (
                      <option key={model.id} value={model.id}>
                        {model.name}
                      </option>
                    ))}
                </NativeSelect>
              </Field>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

function ModelEditor({
  draft,
  settings,
  busy,
  onChange,
  onCancel,
  onSubmit,
}: {
  draft: Draft;
  settings: ModelSettings;
  busy: boolean;
  onChange: (draft: Draft) => void;
  onCancel: () => void;
  onSubmit: (event: React.FormEvent) => void;
}) {
  const fields = settings.catalog.fields[draft.provider] ?? [];
  const set = (change: Partial<Draft>) => onChange({ ...draft, ...change });
  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4 border-t border-border pt-4">
      <h3 className="settings-card-title">{draft.id ? `Edit ${draft.name}` : "Add a model"}</h3>
      <div className="grid grid-cols-2 gap-4 max-[720px]:grid-cols-1">
        <Field label="Name" htmlFor="model-name" hint="Lower-case letters, digits and hyphens, for example fast-gemini.">
          <Input id="model-name" required value={draft.name} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field label="Provider" htmlFor="model-provider">
          <NativeSelect
            id="model-provider"
            value={draft.provider}
            onChange={(e) =>
              set({ provider: e.target.value, model: settings.catalog.defaults[e.target.value] ?? "", config: {}, secrets: {}, secretStatus: {} })
            }
          >
            {settings.catalog.providers.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <Field label="Model" htmlFor="model-model">
          <Input id="model-model" required value={draft.model} spellCheck={false} onChange={(e) => set({ model: e.target.value })} />
        </Field>
        {fields.map((field) => {
          const secret = field.kind === "secret";
          const saved = secret ? draft.secretStatus[field.key] : undefined;
          return (
            <Field
              key={field.key}
              label={`${field.label}${field.required ? " *" : ""}`}
              htmlFor={`model-field-${field.key}`}
              hint={saved ? `Saved: ${saved}. Leave empty to keep it.` : field.help}
            >
              <Input
                id={`model-field-${field.key}`}
                type={secret ? "password" : "text"}
                autoComplete={secret ? "off" : undefined}
                spellCheck={false}
                placeholder={field.placeholder}
                value={secret ? (draft.secrets[field.key] ?? "") : (draft.config[field.key] ?? "")}
                onChange={(e) =>
                  secret
                    ? set({ secrets: { ...draft.secrets, [field.key]: e.target.value } })
                    : set({ config: { ...draft.config, [field.key]: e.target.value } })
                }
              />
            </Field>
          );
        })}
        <Field label="Input price (USD per 1M tokens)" htmlFor="model-input-price" hint="Leave both prices empty to use the catalog price.">
          <Input
            id="model-input-price"
            type="number"
            inputMode="decimal"
            min={0}
            max={1000}
            step="any"
            value={draft.inputPrice}
            onChange={(e) => set({ inputPrice: e.target.value })}
          />
        </Field>
        <Field label="Output price (USD per 1M tokens)" htmlFor="model-output-price" hint="Use 0 for a model on your own hardware.">
          <Input
            id="model-output-price"
            type="number"
            inputMode="decimal"
            min={0}
            max={1000}
            step="any"
            value={draft.outputPrice}
            onChange={(e) => set({ outputPrice: e.target.value })}
          />
        </Field>
      </div>
      <Checkbox label="Use this model" checked={draft.enabled} onCheckedChange={(checked) => set({ enabled: checked === true })} />
      <div className="flex justify-end gap-2 max-[720px]:flex-col max-[720px]:items-stretch">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? "Saving…" : "Save model"}
        </Button>
      </div>
    </form>
  );
}
