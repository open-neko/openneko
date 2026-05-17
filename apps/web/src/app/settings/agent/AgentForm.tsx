"use client";

import { useMemo, useState } from "react";
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
type SettingsPayload = {
  primary: ProviderConfig;
  research: ProviderConfig;
  options: { primary: readonly ProviderOption[]; research: readonly ProviderOption[] };
  defaults: { primary: Record<string, string>; research: Record<string, string> };
  fields: { primary: Record<string, Field[]>; research: Record<string, Field[]> };
};
const INPUT_CLS =
  "px-[13px] py-[11px] sm:px-3.5 sm:py-[13px] rounded-xl border-[1.5px] border-border bg-bg text-text text-base sm:text-[15px] font-body outline-none transition-all duration-200 focus:border-accent focus:shadow-[0_0_0_3px_rgba(107,92,231,0.08)]";
const FIELD_CLS = "flex flex-col gap-2";
const LABEL_CLS = "text-[14px] font-semibold text-text";
const HELP_CLS = "text-[13px] text-text3 leading-[1.45]";

type AgentBackendOption = { value: "hermes" | "claude-agent"; label: string; description: string };
type AgentSettingsPayload = {
  agent: {
    source: "org" | "default";
    backend: "hermes" | "claude-agent";
    globalCap: number;
  };
  options: readonly AgentBackendOption[];
  defaults: { globalCap: number };
};

const CLAUDE_MODEL_DEFAULT = "claude-opus-4-7";

export default function AgentForm({
  initial,
}: {
  initial: { agent: AgentSettingsPayload; providers: SettingsPayload };
}) {
  const [backend, setBackend] = useState(initial.agent.agent.backend);
  const [concurrentJobs, setConcurrentJobs] = useState(String(initial.agent.agent.globalCap));
  const [primary, setPrimary] = useState({
    provider: initial.providers.primary.provider,
    model: initial.providers.primary.model,
    config: stringRecord(initial.providers.primary.config),
    secretStatus: initial.providers.primary.secretStatus,
    secretsInput: {} as Record<string, string>,
    clearedSecrets: {} as Record<string, boolean>,
  });
  const [saving, setSaving] = useState(false);

  const providerOptions = useMemo(() => {
    if (backend === "claude-agent") {
      return initial.providers.options.primary.filter((o) => o.value === "anthropic");
    }
    return initial.providers.options.primary;
  }, [backend, initial.providers.options.primary]);

  const fields: Field[] = initial.providers.fields.primary[primary.provider] ?? [];

  const onBackendChange = (next: "hermes" | "claude-agent") => {
    setBackend(next);
    if (next === "claude-agent" && primary.provider !== "anthropic") {
      setPrimary({
        provider: "anthropic",
        model: CLAUDE_MODEL_DEFAULT,
        config: {},
        secretStatus: {},
        secretsInput: {},
        clearedSecrets: {},
      });
    }
  };

  const onPrimaryProviderChange = (next: string) => {
    setPrimary({
      provider: next,
      model: initial.providers.defaults.primary[next] ?? "",
      config: {},
      secretStatus: {},
      secretsInput: {},
      clearedSecrets: {},
    });
  };

  async function save() {
    setSaving(true);
    try {
      const cap = Number(concurrentJobs) || initial.agent.defaults.globalCap;
      const agentRes = await fetch("/api/settings/agent", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ backend, globalCap: cap }),
      });
      const agentBody = await agentRes.json();
      if (!agentRes.ok) throw new Error(agentBody.error ?? "Agent backend save failed");

      const secretsPayload: Record<string, string | null> = {};
      const configPayload: Record<string, string> = {};
      for (const field of fields) {
        if (field.kind === "secret") {
          const input = primary.secretsInput[field.key]?.trim();
          if (input) secretsPayload[field.key] = input;
          else if (primary.clearedSecrets[field.key]) secretsPayload[field.key] = null;
        } else {
          configPayload[field.key] = primary.config[field.key] ?? "";
        }
      }

      const providerRes = await fetch("/api/settings/provider", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          scope: "primary",
          provider: primary.provider,
          model: primary.model,
          enabled: true,
          config: configPayload,
          secrets: secretsPayload,
        }),
      });
      const providerBody = await providerRes.json();
      if (!providerRes.ok) throw new Error(providerBody.error ?? "Primary provider save failed");

      toast.success("Agent settings saved.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="root">
      <AppHeader back={{ href: "/settings", label: "All settings" }} />
      <div className="greet">Agent.</div>
      <div className="greet-sub mb-6">
        Pick the runtime that drives the metric agent and the model it uses.
      </div>

      <section className="settings-card">
        <div className="grid gap-4 mt-4">
          <label className={FIELD_CLS}>
            <span className={LABEL_CLS}>Backend</span>
            <Select
              value={backend}
              onChange={(v) => onBackendChange(v as "hermes" | "claude-agent")}
              options={initial.agent.options}
              ariaLabel="Agent backend"
            />
            <span className={HELP_CLS}>
              {initial.agent.options.find((o) => o.value === backend)?.description}
            </span>
          </label>

          <div className="settings-grid">
            <label className={FIELD_CLS}>
              <span className={LABEL_CLS}>Provider</span>
              <Select
                value={primary.provider}
                onChange={onPrimaryProviderChange}
                options={providerOptions}
                disabled={backend === "claude-agent"}
                ariaLabel="Primary provider"
              />
              {backend === "claude-agent" && (
                <span className={HELP_CLS}>Locked because Agent backend = Claude Agent.</span>
              )}
            </label>
            <label className={FIELD_CLS}>
              <span className={LABEL_CLS}>Model</span>
              <input
                className={INPUT_CLS}
                value={primary.model}
                onChange={(e) => setPrimary((p) => ({ ...p, model: e.target.value }))}
              />
            </label>
          </div>

          <label className={FIELD_CLS}>
            <span className={LABEL_CLS}>Concurrent jobs</span>
            <input
              className={INPUT_CLS}
              type="number"
              min={1}
              max={1000}
              value={concurrentJobs}
              onChange={(e) => setConcurrentJobs(e.target.value)}
            />
            <span className={HELP_CLS}>
              How many metric jobs the worker runs in parallel. Worker restart applies changes.
            </span>
          </label>

          {fields.map((field) => {
            const masked = primary.secretStatus[field.key];
            const isSecret = field.kind === "secret";
            const value = isSecret
              ? primary.secretsInput[field.key] ?? ""
              : (primary.config[field.key] as string) ?? "";

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
                      setPrimary((p) => ({
                        ...p,
                        secretsInput: { ...p.secretsInput, [field.key]: e.target.value },
                        clearedSecrets: { ...p.clearedSecrets, [field.key]: false },
                      }));
                    } else {
                      setPrimary((p) => ({
                        ...p,
                        config: { ...p.config, [field.key]: e.target.value },
                      }));
                    }
                  }}
                />
                {field.help && <span className={HELP_CLS}>{field.help}</span>}
                {isSecret && masked && !primary.clearedSecrets[field.key] && (
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-text3 text-[13px]">Saved: {masked}</span>
                    <button
                      type="button"
                      className="border-0 bg-transparent text-[#b05555] cursor-pointer text-[13px] font-semibold"
                      onClick={() =>
                        setPrimary((p) => ({
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
