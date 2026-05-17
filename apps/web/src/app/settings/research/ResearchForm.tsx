"use client";

import { useState } from "react";
import AppHeader from "@/components/AppHeader";
import { toast } from "sonner";
import Select from "@/components/Select";
import { Button } from "@/components/ui/Button";

type ProviderOption = { value: string; label: string; description: string };
type Field = {
  key: string;
  label: string;
  kind: "text" | "secret" | "url";
  required?: boolean;
  placeholder?: string;
  help?: string;
};
type ProviderConfig = {
  scope: "primary" | "research";
  source: "org" | "env" | "default";
  provider: string;
  model: string;
  enabled: boolean;
  config: Record<string, unknown>;
  secretStatus: Record<string, string>;
};
const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";
const FIELD_CLS = "flex flex-col gap-2";
const LABEL_CLS = "text-[14px] font-semibold text-text";
const HELP_CLS = "text-[13px] text-text3 leading-[1.45]";

type SettingsPayload = {
  primary: ProviderConfig;
  research: ProviderConfig;
  options: { primary: readonly ProviderOption[]; research: readonly ProviderOption[] };
  defaults: { primary: Record<string, string>; research: Record<string, string> };
  fields: { primary: Record<string, Field[]>; research: Record<string, Field[]> };
};

export default function ResearchForm({ initial }: { initial: SettingsPayload }) {
  const initialResearch = initial.research;
  const [enabled, setEnabled] = useState(
    initialResearch.enabled && initialResearch.provider !== "disabled",
  );
  const initialProvider =
    initialResearch.provider === "disabled"
      ? initial.options.research.find((o) => o.value !== "disabled")?.value ?? "perplexity"
      : initialResearch.provider;
  const [research, setResearch] = useState({
    provider: initialProvider,
    model:
      initialResearch.provider === "disabled"
        ? initial.defaults.research[initialProvider] ?? ""
        : initialResearch.model,
    config: stringRecord(initialResearch.config),
    secretStatus: initialResearch.secretStatus,
    secretsInput: {} as Record<string, string>,
    clearedSecrets: {} as Record<string, boolean>,
  });
  const [saving, setSaving] = useState(false);

  const fields: Field[] = initial.fields.research[research.provider] ?? [];
  const providerOptions = initial.options.research.filter((o) => o.value !== "disabled");

  async function save() {
    setSaving(true);
    try {
      const body = enabled
        ? {
            scope: "research",
            provider: research.provider,
            model: research.model,
            enabled: true,
            config: research.config,
            secrets: secretsPayload(),
          }
        : {
            scope: "research",
            provider: "disabled",
            model: "",
            enabled: false,
            config: {},
            secrets: {},
          };
      const res = await fetch("/api/settings/provider", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const json = await res.json();
      if (!res.ok) throw new Error(json.error ?? "Save failed");
      toast.success(enabled ? "Research enabled and saved." : "Research disabled.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  function secretsPayload(): Record<string, string | null> {
    const out: Record<string, string | null> = {};
    for (const field of fields) {
      if (field.kind !== "secret") continue;
      const input = research.secretsInput[field.key]?.trim();
      if (input) out[field.key] = input;
      else if (research.clearedSecrets[field.key]) out[field.key] = null;
    }
    return out;
  }

  return (
    <div className="root">
      <AppHeader back={{ href: "/settings", label: "All settings" }} />
      <div className="greet">Industry research.</div>
      <div className="greet-sub mb-6">
        Optional. When enabled, OpenNeko enriches the business profile with industry context during onboarding.
      </div>

      <section className="settings-card">
        <label className="inline-flex items-center gap-2.5 mt-[18px] text-text2 text-[15px]">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>Enable industry research</span>
        </label>

        {enabled && (
          <div className="grid gap-4 mt-4">
            <div className="settings-grid">
              <label className={FIELD_CLS}>
                <span className={LABEL_CLS}>Provider</span>
                <Select
                  value={research.provider}
                  onChange={(v) =>
                    setResearch({
                      provider: v,
                      model: initial.defaults.research[v] ?? "",
                      config: {},
                      secretStatus: {},
                      secretsInput: {},
                      clearedSecrets: {},
                    })
                  }
                  options={providerOptions}
                  ariaLabel="Research provider"
                />
              </label>
              <label className={FIELD_CLS}>
                <span className={LABEL_CLS}>Model</span>
                <input
                  className={INPUT_CLS}
                  value={research.model}
                  onChange={(e) => setResearch((p) => ({ ...p, model: e.target.value }))}
                />
              </label>
            </div>

            {fields.map((field) => {
              const masked = research.secretStatus[field.key];
              const isSecret = field.kind === "secret";
              const value = isSecret
                ? research.secretsInput[field.key] ?? ""
                : (research.config[field.key] as string) ?? "";

              return (
                <label key={field.key} className={FIELD_CLS}>
                  <span className={LABEL_CLS}>
                    {field.label}
                    {field.required ? " *" : ""}
                  </span>
                  <input
                    className={INPUT_CLS}
                    type={field.kind === "secret" ? "password" : "text"}
                    value={value}
                    placeholder={field.placeholder}
                    onChange={(e) => {
                      if (isSecret) {
                        setResearch((p) => ({
                          ...p,
                          secretsInput: { ...p.secretsInput, [field.key]: e.target.value },
                          clearedSecrets: { ...p.clearedSecrets, [field.key]: false },
                        }));
                      } else {
                        setResearch((p) => ({
                          ...p,
                          config: { ...p.config, [field.key]: e.target.value },
                        }));
                      }
                    }}
                  />
                  {field.help && <span className={HELP_CLS}>{field.help}</span>}
                  {isSecret && masked && !research.clearedSecrets[field.key] && (
                    <div className="flex items-center justify-between gap-3">
                      <span className="text-text3 text-[13px]">Saved: {masked}</span>
                      <button
                        type="button"
                        className="border-0 bg-transparent text-[#b05555] cursor-pointer text-[13px] font-semibold"
                        onClick={() =>
                          setResearch((p) => ({
                            ...p,
                            secretsInput: { ...p.secretsInput, [field.key]: "" },
                            clearedSecrets: { ...p.clearedSecrets, [field.key]: true },
                          }))
                        }
                      >
                        Clear saved value
                      </button>
                    </div>
                  )}
                </label>
              );
            })}
          </div>
        )}

        <div className="flex justify-end gap-2.5 mt-5 max-[720px]:flex-col max-[720px]:items-stretch [&>button]:max-[720px]:w-full">
          <Button variant="primary" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save settings"}
          </Button>
        </div>
      </section>
    </div>
  );
}

function stringRecord(input: Record<string, unknown>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(input).map(([k, v]) => [k, v == null ? "" : String(v)]),
  );
}
