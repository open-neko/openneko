"use client";

import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import Select from "@/components/Select";

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
    <div className="root" style={{ paddingTop: 44 }}>
      <div className="settings-topbar">
        <div>
          <div className="brand">
            <img className="brand-icon" src="/cat.png" alt="" width={32} height={32} />
            <span className="brand-name">Neko</span>
          </div>
          <div className="greet" style={{ marginTop: 28 }}>Industry research.</div>
          <div className="greet-sub">
            Optional. When enabled, Neko enriches the business profile with industry context during onboarding.
          </div>
        </div>
        <Link className="settings-backlink inline-flex items-center gap-2 whitespace-nowrap" href="/settings">
          <span aria-hidden="true" className="settings-backlink-arrow text-base leading-none">←</span>
          <span>All settings</span>
        </Link>
      </div>

      <section className="settings-card">
        <label className="settings-toggle">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>Enable industry research</span>
        </label>

        {enabled && (
          <div className="settings-field-stack">
            <div className="settings-grid">
              <label className="settings-field">
                <span className="settings-label">Provider</span>
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
              <label className="settings-field">
                <span className="settings-label">Model</span>
                <input
                  className="settings-input"
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
                <label key={field.key} className="settings-field">
                  <span className="settings-label">
                    {field.label}
                    {field.required ? " *" : ""}
                  </span>
                  <input
                    className="settings-input"
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
                  {field.help && <span className="settings-help">{field.help}</span>}
                  {isSecret && masked && !research.clearedSecrets[field.key] && (
                    <div className="settings-secret-row">
                      <span className="settings-secret-note">Saved: {masked}</span>
                      <button
                        type="button"
                        className="settings-clear"
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

        <div className="settings-actions">
          <button type="button" className="pill on" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save settings"}
          </button>
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
