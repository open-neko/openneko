"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
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

type DataSourcePayload = {
  source: "org" | "unset";
  kind: string;
  graphqlUrl: string;
  mcpUrl: string;
  label: string;
};

type Initial = {
  dataSource: DataSourcePayload;
  providers: SettingsPayload;
  agent: AgentSettingsPayload;
  passwordChanged: boolean;
};

// Step 0 ("Password") is shown only when the admin hasn't picked one yet.
// Once changed, ~/.config/openneko/config.json has pg.password and we skip
// straight to Data on subsequent visits.
const STEPS_WITH_PASSWORD = ["Password", "Data", "Agent", "Research"] as const;
const STEPS_WITHOUT_PASSWORD = ["Data", "Agent", "Research"] as const;
const CLAUDE_MODEL_DEFAULT = "claude-opus-4-7";

export default function SetupWizard({ initial }: { initial: Initial }) {
  const router = useRouter();
  const STEPS = initial.passwordChanged
    ? STEPS_WITHOUT_PASSWORD
    : STEPS_WITH_PASSWORD;

  const [step, setStep] = useState(0);

  // Step 0 (only shown when initial.passwordChanged === false): set DB password.
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [savingPassword, setSavingPassword] = useState(false);

  type StepName = "password" | "data" | "agent" | "research";
  const stepName: StepName = (() => {
    if (initial.passwordChanged) {
      return (["data", "agent", "research"] as const)[step] ?? "data";
    }
    return (["password", "data", "agent", "research"] as const)[step] ?? "password";
  })();

  // Step 1: data source — one root URL, /api/v1/{graphql,mcp} derived on save.
  // Pre-fill the localhost default when nothing is configured yet so the
  // documented AdventureWorks-on-:8080 happy path is one Continue away.
  const [data, setData] = useState({
    rootUrl: deriveRoot(initial.dataSource.graphqlUrl) || "http://localhost:8080",
    label: initial.dataSource.label || "primary",
  });
  const [savingData, setSavingData] = useState(false);
  const [testingData, setTestingData] = useState(false);

  // Step 2: backend + primary provider
  const [backend, setBackend] = useState<"hermes" | "claude-agent">(initial.agent.agent.backend);
  const [concurrentJobs, setConcurrentJobs] = useState(String(initial.agent.agent.globalCap));
  const [primary, setPrimary] = useState({
    provider: initial.providers.primary.provider,
    model: initial.providers.primary.model,
    config: stringRecord(initial.providers.primary.config),
    secrets: {} as Record<string, string>,
  });
  const [savingPrimary, setSavingPrimary] = useState(false);

  // Step 3: research
  const [researchEnabled, setResearchEnabled] = useState(
    initial.providers.research.enabled && initial.providers.research.provider !== "disabled",
  );
  const initialResearchProvider =
    initial.providers.research.provider === "disabled"
      ? initial.providers.options.research.find((o) => o.value !== "disabled")?.value ?? "perplexity"
      : initial.providers.research.provider;
  const [research, setResearch] = useState({
    provider: initialResearchProvider,
    model:
      initial.providers.research.provider === "disabled"
        ? initial.providers.defaults.research[initialResearchProvider] ?? ""
        : initial.providers.research.model,
    config: stringRecord(initial.providers.research.config),
    secrets: {} as Record<string, string>,
  });
  const [savingResearch, setSavingResearch] = useState(false);
  const [finishing, setFinishing] = useState(false);

  // Per-step inline validation errors. Cleared when the user edits the
  // step's inputs or moves to a different step.
  const [dataError, setDataError] = useState<string | null>(null);
  const [primaryError, setPrimaryError] = useState<string | null>(null);
  const [researchError, setResearchError] = useState<string | null>(null);

  // Backend-coupled provider lock for step 2
  const providerOptions = useMemo<ProviderOption[]>(() => {
    if (backend === "claude-agent") {
      return initial.providers.options.primary.filter((o) => o.value === "anthropic");
    }
    return [...initial.providers.options.primary];
  }, [backend, initial.providers.options.primary]);

  const primaryFields: Field[] =
    initial.providers.fields.primary[primary.provider] ?? [];
  const researchFields: Field[] =
    initial.providers.fields.research[research.provider] ?? [];

  const onBackendChange = (next: "hermes" | "claude-agent") => {
    setBackend(next);
    if (next === "claude-agent" && primary.provider !== "anthropic") {
      setPrimary({
        provider: "anthropic",
        model: CLAUDE_MODEL_DEFAULT,
        config: {},
        secrets: {},
      });
    }
  };

  const onPrimaryProviderChange = (next: string) => {
    setPrimary({
      provider: next,
      model: initial.providers.defaults.primary[next] ?? "",
      config: {},
      secrets: {},
    });
  };

  // ---------------- Password step actions ----------------

  async function savePasswordAndAdvance() {
    setSavingPassword(true);
    try {
      const res = await fetch("/api/admin/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: newPassword }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Password change failed");
      toast.success("Database password updated.");
      setNewPassword("");
      setConfirmPassword("");
      setStep(step + 1);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSavingPassword(false);
    }
  }

  // ---------------- Step 1 actions ----------------

  async function testData() {
    setTestingData(true);
    try {
      const { graphqlUrl, mcpUrl } = deriveEndpoints(data.rootUrl);
      const res = await fetch("/api/settings/data-source/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ graphqlUrl, mcpUrl }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Test failed");
      toast.success(
        body.mcpOk === false
          ? "GraphQL reachable. MCP unreachable."
          : "Connection looks good.",
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setTestingData(false);
    }
  }

  async function saveDataAndAdvance() {
    setSavingData(true);
    setDataError(null);
    try {
      const { graphqlUrl, mcpUrl } = deriveEndpoints(data.rootUrl);
      // Gate: live connectivity test before save. Reuses the same endpoint
      // the explicit Test button uses; on failure we surface the error
      // inline and DO NOT save — the user shouldn't move past a step with
      // a broken URL only to fail mid-onboarding minutes later.
      const testRes = await fetch("/api/settings/data-source/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ graphqlUrl, mcpUrl }),
      });
      const testBody = await testRes.json().catch(() => ({}));
      if (!testRes.ok) {
        throw new Error(testBody.error ?? "Connection test failed.");
      }
      const res = await fetch("/api/settings/data-source", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ graphqlUrl, mcpUrl, label: data.label }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Save failed");
      toast.success(
        testBody.mcpOk === false
          ? "Data source saved. (MCP unreachable — fine for the agent path.)"
          : "Data source saved.",
      );
      setStep(step + 1);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setDataError(msg);
    } finally {
      setSavingData(false);
    }
  }

  // ---------------- Step 2 actions ----------------

  async function savePrimaryAndAdvance() {
    setSavingPrimary(true);
    setPrimaryError(null);
    try {
      // Gate: validate the provider key with a real one-shot LLM call
      // BEFORE saving anything. A bad key would otherwise pass step 2
      // silently and only manifest minutes later as a failed
      // business_profile_build job — surfacing the error here saves
      // the user from a long-tail failure.
      const testRes = await fetch("/api/settings/provider/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          scope: "primary",
          provider: primary.provider,
          model: primary.model,
          enabled: true,
          config: primary.config,
          secrets: primary.secrets,
        }),
      });
      const testBody = await testRes.json().catch(() => ({}));
      if (!testRes.ok) {
        throw new Error(testBody.error ?? "Provider test failed.");
      }
      // Save backend choice + concurrency. One UI value drives both the
      // worker-wide pull cap and the in-process Claude Agent semaphore.
      const cap = Number(concurrentJobs) || initial.agent.defaults.globalCap;
      const agentRes = await fetch("/api/settings/agent", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ backend, globalCap: cap }),
      });
      if (!agentRes.ok) {
        const body = await agentRes.json();
        throw new Error(body.error ?? "Agent backend save failed");
      }
      // Save primary provider (with secrets)
      const providerRes = await fetch("/api/settings/provider", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          scope: "primary",
          provider: primary.provider,
          model: primary.model,
          enabled: true,
          config: primary.config,
          secrets: primary.secrets,
        }),
      });
      const providerBody = await providerRes.json();
      if (!providerRes.ok) {
        throw new Error(providerBody.error ?? "Primary provider save failed");
      }
      toast.success("Agent and provider saved.");
      setStep(step + 1);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setPrimaryError(msg);
    } finally {
      setSavingPrimary(false);
    }
  }

  // ---------------- Step 3 actions ----------------

  async function saveResearchOnly() {
    setSavingResearch(true);
    try {
      const body = researchEnabled
        ? {
            scope: "research",
            provider: research.provider,
            model: research.model,
            enabled: true,
            config: research.config,
            secrets: research.secrets,
          }
        : {
            scope: "research",
            provider: "disabled",
            model: "",
            enabled: false,
            config: {},
            secrets: {},
          };
      // Gate: validate the research key with a real one-shot call before
      // saving — only when enabled. Skipping the test for the explicit
      // "disabled" path lets users finish setup without a Perplexity key.
      if (researchEnabled) {
        const testRes = await fetch("/api/settings/provider/test", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        const testBody = await testRes.json().catch(() => ({}));
        if (!testRes.ok) {
          throw new Error(testBody.error ?? "Research provider test failed.");
        }
      }
      const res = await fetch("/api/settings/provider", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const json = await res.json();
      if (!res.ok) throw new Error(json.error ?? "Save failed");
    } finally {
      setSavingResearch(false);
    }
  }

  async function finish(skipResearch: boolean) {
    setFinishing(true);
    setResearchError(null);
    try {
      if (!skipResearch) await saveResearchOnly();
      else {
        // When skipping, persist the explicit "disabled" state so the
        // worker won't try to chain industry research later.
        await fetch("/api/settings/provider", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            scope: "research",
            provider: "disabled",
            model: "",
            enabled: false,
            config: {},
            secrets: {},
          }),
        });
      }

      const res = await fetch("/settings/finish", { method: "POST" });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "Finish failed");
      toast.success("Setup complete. Now describe your business.");
      // Hard navigation: router.push reuses Next's RSC client cache, which
      // may still have the pre-finish /onboarding response (where it
      // redirected back to /settings because setup_complete_at was null).
      // window.location.assign forces a fresh server fetch.
      window.location.assign("/onboarding");
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setResearchError(msg);
      setFinishing(false);
    }
  }

  // ---------------- Render ----------------

  return (
    <div className="root">
      <AppHeader />
      <div className="greet">Get your workspace ready.</div>
      <div className="greet-sub">
        A few quick steps for whoever sets up the data plumbing. Your business team takes over
        once this is done.
      </div>

      <Stepper current={step} steps={STEPS} />

      {stepName === "password" && (
        <Step
          title="Choose a database password"
          description="OpenNeko's storage ships with a default password. Pick something only you know — you won't need to enter it again."
        >
          <Field label="New password (min 8 chars)">
            <input
              className="settings-input"
              type="password"
              value={newPassword}
              autoComplete="new-password"
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </Field>
          <Field label="Confirm password">
            <input
              className="settings-input"
              type="password"
              value={confirmPassword}
              autoComplete="new-password"
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
            {confirmPassword.length > 0 && confirmPassword !== newPassword && (
              <span className="settings-help" style={{ color: "#c33" }}>
                Passwords don&apos;t match.
              </span>
            )}
          </Field>
          <div className="settings-actions">
            <button
              type="button"
              className="pill on"
              onClick={savePasswordAndAdvance}
              disabled={
                savingPassword ||
                newPassword.length < 8 ||
                newPassword !== confirmPassword
              }
            >
              {savingPassword ? "Saving…" : "Continue"}
            </button>
          </div>
        </Step>
      )}

      {stepName === "data" && (
        <Step
          title="Connect your data"
          description="Point OpenNeko at your GraphJin server. The metric agent uses this for every read."
        >
          <Field label="GraphJin URL *">
            <input
              className="settings-input"
              value={data.rootUrl}
              placeholder="http://localhost:8080"
              onChange={(e) => {
                setDataError(null);
                setData((p) => ({ ...p, rootUrl: e.target.value }));
              }}
            />
            <span className="settings-help">
              Just the base URL — OpenNeko handles the GraphQL and MCP endpoints automatically.
            </span>
          </Field>
          <Field label="Label">
            <input
              className="settings-input"
              value={data.label}
              placeholder="primary"
              onChange={(e) => setData((p) => ({ ...p, label: e.target.value }))}
            />
          </Field>
          <InlineError message={dataError} />
          <div className="settings-actions">
            <button
              type="button"
              className="pill"
              onClick={testData}
              disabled={testingData || !data.rootUrl.trim()}
            >
              {testingData ? "Testing…" : "Test connection"}
            </button>
            <button
              type="button"
              className="pill on"
              onClick={saveDataAndAdvance}
              disabled={savingData || !data.rootUrl.trim()}
            >
              {savingData ? "Saving…" : "Continue"}
            </button>
          </div>
        </Step>
      )}

      {stepName === "agent" && (
        <Step
          title="Choose the agent"
          description="The agent runs the metric queries. Hermes works with any provider; the Claude Agent is locked to Anthropic."
        >
          <Field label="Backend">
            <Select
              value={backend}
              onChange={(v) => onBackendChange(v as "hermes" | "claude-agent")}
              options={initial.agent.options}
              ariaLabel="Agent backend"
            />
            <span className="settings-help">
              {initial.agent.options.find((o) => o.value === backend)?.description}
            </span>
          </Field>

          <div className="settings-grid">
            <Field label="Provider">
              <Select
                value={primary.provider}
                onChange={onPrimaryProviderChange}
                options={providerOptions}
                disabled={backend === "claude-agent"}
                ariaLabel="Primary provider"
              />
              {backend === "claude-agent" && (
                <span className="settings-help">Locked because Agent backend = Claude Agent.</span>
              )}
            </Field>
            <Field label="Model">
              <input
                className="settings-input"
                value={primary.model}
                onChange={(e) => setPrimary((p) => ({ ...p, model: e.target.value }))}
              />
            </Field>
          </div>

          {primaryFields.map((field) => (
            <ProviderFieldInput
              key={field.key}
              field={field}
              value={
                field.kind === "secret"
                  ? primary.secrets[field.key] ?? ""
                  : (primary.config[field.key] as string) ?? ""
              }
              onChange={(v) => {
                if (field.kind === "secret") {
                  setPrimary((p) => ({ ...p, secrets: { ...p.secrets, [field.key]: v } }));
                } else {
                  setPrimary((p) => ({ ...p, config: { ...p.config, [field.key]: v } }));
                }
              }}
            />
          ))}

          <Field label="Concurrent jobs">
            <input
              className="settings-input"
              type="number"
              min={1}
              max={1000}
              value={concurrentJobs}
              onChange={(e) => setConcurrentJobs(e.target.value)}
            />
            <span className="settings-help">
              How many metric jobs the worker runs in parallel. Worker restart applies changes.
            </span>
          </Field>

          <InlineError message={primaryError} />

          <div className="settings-actions">
            <button type="button" className="pill" onClick={() => setStep(step - 1)} disabled={savingPrimary}>
              Back
            </button>
            <button
              type="button"
              className="pill on"
              onClick={savePrimaryAndAdvance}
              disabled={savingPrimary}
            >
              {savingPrimary ? "Validating & saving…" : "Continue"}
            </button>
          </div>
        </Step>
      )}

      {stepName === "research" && (
        <Step
          title="Research (optional)"
          description="Lets the system pull industry context from Perplexity once your business team submits the onboarding profile. Leave the toggle off to set this up later."
        >
          <label className="settings-toggle">
            <input
              type="checkbox"
              checked={researchEnabled}
              onChange={(e) => setResearchEnabled(e.target.checked)}
            />
            <span>Enable industry research</span>
          </label>

          {researchEnabled && (
            <>
              <div className="settings-grid">
                <Field label="Provider">
                  <Select
                    value={research.provider}
                    onChange={(v) =>
                      setResearch({
                        provider: v,
                        model: initial.providers.defaults.research[v] ?? "",
                        config: {},
                        secrets: {},
                      })
                    }
                    options={initial.providers.options.research.filter(
                      (option) => option.value !== "disabled",
                    )}
                    ariaLabel="Research provider"
                  />
                </Field>
                <Field label="Model">
                  <input
                    className="settings-input"
                    value={research.model}
                    onChange={(e) => setResearch((p) => ({ ...p, model: e.target.value }))}
                  />
                </Field>
              </div>

              {researchFields.map((field) => (
                <ProviderFieldInput
                  key={field.key}
                  field={field}
                  value={
                    field.kind === "secret"
                      ? research.secrets[field.key] ?? ""
                      : (research.config[field.key] as string) ?? ""
                  }
                  onChange={(v) => {
                    if (field.kind === "secret") {
                      setResearch((p) => ({
                        ...p,
                        secrets: { ...p.secrets, [field.key]: v },
                      }));
                    } else {
                      setResearch((p) => ({
                        ...p,
                        config: { ...p.config, [field.key]: v },
                      }));
                    }
                  }}
                />
              ))}
            </>
          )}

          <InlineError message={researchError} />

          <div className="settings-actions">
            <button type="button" className="pill" onClick={() => setStep(step - 1)} disabled={finishing || savingResearch}>
              Back
            </button>
            <button
              type="button"
              className="pill on"
              onClick={() => finish(!researchEnabled)}
              disabled={finishing || savingResearch}
            >
              {finishing || savingResearch ? "Saving…" : "Finish setup"}
            </button>
          </div>
        </Step>
      )}
    </div>
  );
}

function Stepper({ current, steps }: { current: number; steps: readonly string[] }) {
  return (
    <div style={{ display: "flex", gap: 12, margin: "32px 0 24px", color: "var(--muted)" }}>
      {steps.map((label, i) => (
        <div
          key={label}
          style={{
            padding: "6px 14px",
            borderRadius: 999,
            border: "1px solid var(--border)",
            background: i === current ? "var(--accent)" : "transparent",
            color: i === current ? "var(--bg)" : "var(--muted)",
            fontSize: 13,
          }}
        >
          {i + 1}. {label}
        </div>
      ))}
    </div>
  );
}

function Step({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <div>
          <h2 className="settings-card-title">{title}</h2>
          <p className="settings-card-copy">{description}</p>
        </div>
      </div>
      <div className="settings-field-stack">{children}</div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="settings-field">
      <span className="settings-label">{label}</span>
      {children}
    </label>
  );
}

function InlineError({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div role="alert" className="settings-error">
      {message}
    </div>
  );
}

function ProviderFieldInput({
  field,
  value,
  onChange,
}: {
  field: Field;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="settings-field">
      <span className="settings-label">
        {field.label}
        {field.required ? " *" : ""}
      </span>
      <input
        className="settings-input"
        type={field.kind === "secret" ? "password" : "text"}
        value={value}
        placeholder={field.placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
      {field.help && <span className="settings-help">{field.help}</span>}
    </label>
  );
}

function stringRecord(input: Record<string, unknown>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(input).map(([k, v]) => [k, v == null ? "" : String(v)]),
  );
}

const GRAPHQL_SUFFIX = "/api/v1/graphql";
const MCP_SUFFIX = "/api/v1/mcp";

function deriveEndpoints(rootUrl: string): { graphqlUrl: string; mcpUrl: string } {
  const root = deriveRoot(rootUrl);
  return { graphqlUrl: `${root}${GRAPHQL_SUFFIX}`, mcpUrl: `${root}${MCP_SUFFIX}` };
}

// Accept whatever the user pastes — bare root, trailing slash, or a full
// GraphJin endpoint URL — and reduce it to a clean root so deriveEndpoints
// can append the canonical suffixes without doubling them.
function deriveRoot(input: string): string {
  let s = input.trim().replace(/\/+$/, "");
  const lower = s.toLowerCase();
  if (lower.endsWith(GRAPHQL_SUFFIX)) s = s.slice(0, -GRAPHQL_SUFFIX.length);
  else if (lower.endsWith(MCP_SUFFIX)) s = s.slice(0, -MCP_SUFFIX.length);
  return s.replace(/\/+$/, "");
}
