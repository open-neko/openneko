"use client";

import { useMemo, useState } from "react";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import { toast } from "sonner";
import Select from "@/components/Select";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, Input } from "@/components/ui/field";
import EgressHostsField, { egressHostLines } from "./EgressHostsField";
import { terminalHint } from "./terminal-hint";
import UnconfinedTurnsField, { type UnconfinedTurnsState } from "./UnconfinedTurnsField";

type ProviderOption = { value: string; label: string; description: string };
type ProviderField = {
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
  fields: { primary: Record<string, ProviderField[]>; research: Record<string, ProviderField[]> };
};
type AgentSettingsPayload = {
  agent: {
    source: "org" | "default";
    backend: string;
    globalCap: number;
    terminal: boolean;
    responderEffort: ResponderEffort;
    egressHosts?: string[];
  };
  egressSuggestions?: string[];
  options: readonly ProviderOption[];
  terminalAvailable: boolean;
  terminalBlocked?: string;
  defaults: { globalCap: number; terminal: boolean; responderEffort: ResponderEffort };
};
type ResponderEffort = "low" | "medium" | "high";

const responderEffortOptions: readonly { value: ResponderEffort; label: string; description: string }[] = [
  { value: "low", label: "Low", description: "Fastest answer. The model writes up what the agent found with little extra thought." },
  { value: "medium", label: "Medium", description: "The model thinks before it writes the answer, so the answer starts later." },
  { value: "high", label: "High", description: "The model thinks longest before it writes the answer." },
];

export default function AgentForm({
  initial,
}: {
  initial: { agent: AgentSettingsPayload; providers: SettingsPayload; unconfined?: UnconfinedTurnsState };
}) {
  const [concurrentJobs, setConcurrentJobs] = useState(String(initial.agent.agent.globalCap));
  const [terminal, setTerminal] = useState(initial.agent.agent.terminal);
  const [responderEffort, setResponderEffort] = useState<ResponderEffort>(
    initial.agent.agent.responderEffort ?? initial.agent.defaults.responderEffort ?? "low",
  );
  const terminalAvailable = initial.agent.terminalAvailable;
  const [egressHosts, setEgressHosts] = useState((initial.agent.agent.egressHosts ?? []).join("\n"));
  const [primary, setPrimary] = useState({
    provider: initial.providers.primary.provider,
    model: initial.providers.primary.model,
    config: stringRecord(initial.providers.primary.config),
    secretStatus: initial.providers.primary.secretStatus,
    secretsInput: {} as Record<string, string>,
    clearedSecrets: {} as Record<string, boolean>,
  });
  const [saving, setSaving] = useState(false);

  const providerOptions = useMemo(
    () => initial.providers.options.primary,
    [initial.providers.options.primary],
  );

  const fields: ProviderField[] = initial.providers.fields.primary[primary.provider] ?? [];

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

      const providerRes = await fetch("/api/v1/settings/provider", {
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

      const cap = Number(concurrentJobs) || initial.agent.defaults.globalCap;
      const agentRes = await fetch("/api/v1/settings/agent", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(
          terminalAvailable
            ? { globalCap: cap, terminal, responderEffort, egressHosts: egressHostLines(egressHosts) }
            : { globalCap: cap, responderEffort },
        ),
      });
      const agentBody = await agentRes.json();
      if (!agentRes.ok) throw new Error(agentBody.error ?? "Agent settings save failed");
      if (Array.isArray(agentBody.egressHosts)) setEgressHosts(agentBody.egressHosts.join("\n"));

      toast.success("Agent settings saved.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="root is-narrow">
      <AppHeader back={{ href: "/admin/settings", label: "Settings" }} />
      <PageHeading
        title="Agent"
        description="Choose the model provider for the whole organization and worker concurrency."
      />

      <section className="settings-card">
        <div className="grid gap-4">
          <div className="settings-grid">
            <Field label="Provider">
              <Select
                id="agent-provider"
                value={primary.provider}
                onChange={onPrimaryProviderChange}
                options={providerOptions}
                ariaLabel="Primary provider"
              />
            </Field>
            <Field label="Model" htmlFor="agent-model">
              <Input
                id="agent-model"
                value={primary.model}
                onChange={(e) => setPrimary((p) => ({ ...p, model: e.target.value }))}
              />
            </Field>
          </div>

          <Field
            label="Concurrent jobs"
            htmlFor="agent-concurrent-jobs"
            hint="How many agent turns run at the same time. Slack and Telegram messages run at most 3 turns at once. The change applies when you save."
          >
            <Input
              id="agent-concurrent-jobs"
              type="number"
              min={1}
              max={1000}
              value={concurrentJobs}
              onChange={(e) => setConcurrentJobs(e.target.value)}
            />
          </Field>

          <Field
            label="Answer effort"
            htmlFor="agent-responder-effort"
            hint="How much the model thinks before it writes the answer from what the agent found. The agent's own steps keep the model's effort. Applies from the next run."
          >
            <Select
              id="agent-responder-effort"
              value={responderEffort}
              onChange={(next) => setResponderEffort(next as ResponderEffort)}
              options={responderEffortOptions}
              ariaLabel="Answer effort"
            />
          </Field>

          <Field
            label="Terminal"
            hint={terminalHint(terminalAvailable, initial.agent.terminalBlocked)}
          >
            <Checkbox
              label="Let the agent use the terminal"
              checked={terminalAvailable && terminal}
              disabled={!terminalAvailable}
              onCheckedChange={(checked) => setTerminal(checked === true)}
            />
          </Field>

          <EgressHostsField
            value={egressHosts}
            onChange={setEgressHosts}
            suggestions={initial.agent.egressSuggestions ?? []}
            available={terminalAvailable}
          />

          {initial.unconfined && <UnconfinedTurnsField initial={initial.unconfined} />}

          {fields.map((field) => {
            const masked = primary.secretStatus[field.key];
            const isSecret = field.kind === "secret";
            const value = isSecret
              ? primary.secretsInput[field.key] ?? ""
              : (primary.config[field.key] as string) ?? "";

            return (
              <Field
                key={field.key}
                label={`${field.label}${field.required ? " *" : ""}`}
                htmlFor={`agent-field-${field.key}`}
                hint={field.help}
              >
                <Input
                  id={`agent-field-${field.key}`}
                  type={field.kind === "secret" ? "password" : "text"}
                  value={value}
                  placeholder={field.placeholder}
                  autoComplete={isSecret ? "off" : undefined}
                  spellCheck={false}
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
                {isSecret && masked && !primary.clearedSecrets[field.key] && (
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-text3 text-ui-body-sm">Saved: {masked}</span>
                    <Button
                      type="button"
                      variant="danger"
                      size="sm"
                      onClick={() =>
                        setPrimary((p) => ({
                          ...p,
                          secretsInput: { ...p.secretsInput, [field.key]: "" },
                          clearedSecrets: { ...p.clearedSecrets, [field.key]: true },
                        }))
                      }
                    >
                      Clear saved value
                    </Button>
                  </div>
                )}
              </Field>
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
