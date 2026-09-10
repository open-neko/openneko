"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { ActionGroup } from "@/components/ui/action-group";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { Field, Input, NativeSelect, Textarea } from "@/components/ui/field";
import { LocalDateTime } from "@/components/ui/local-date-time";
import { Badge, type BadgeVariant } from "@/components/ui/badge";
import CustomPacksAdmin from "./CustomPacksAdmin";

type PackStatus = {
  packId: string;
  version: string;
  status: string;
  readiness: Record<string, { status: string; reason: string | null }>;
  installedAt: string | null;
  lastError: string | null;
};

type DoctorResult = {
  packId: string;
  status: string;
  checks: Array<{ id: string; status: string; detail: string }>;
};

type StoreControl = {
  domain: string;
  automationEligible: boolean;
  enabled: boolean;
  autoExecute: boolean;
  readiness: string;
  readinessReason: string | null;
  readinessMessage: string;
  caps: Record<string, number>;
};

type ActivityItem = {
  id: string;
  kind: "change" | "handoff";
  title: string;
  description: string;
  outcome: "completed" | "reverted" | "awaiting_approval" | "in_progress" | "needs_attention" | "failed" | "cancelled";
  outcomeLabel: string;
  affectedCount: number;
  source: "requested_change" | "automatic_rule" | "test";
  sourceLabel: string;
  isTest: boolean;
  occurredAt: string;
  currentState: string | null;
  technical: {
    reference: string;
    area: string;
    operation: string;
    execution: string;
    originalRequest: string;
    bulkReference: string | null;
    inverseOfReference: string | null;
  };
};

export type StoreManagement = {
  controls: StoreControl[];
  rules: Array<{
    id: string;
    name: string;
    instruction: string;
    domain: string;
    actionKind: string;
    dailyCap: number;
    cooldownSeconds: number;
    enabled: boolean;
    suspendedReason: string | null;
    isTest: boolean;
  }>;
  changesets: Array<{
    id: string;
    domain: string;
    operationId: string;
    executionMode: string;
    status: string;
    summary: string;
    bulkUuid: string | null;
    inverseOfId: string | null;
    createdAt: string;
  }>;
  handoffs: Array<{
    id: string;
    kind: string;
    entityRef: string;
    status: string;
    createdAt: string;
  }>;
  activity: ActivityItem[];
  handoffOnly: { executePath: false; handoffKinds: string[] };
};

type FormState = {
  baseUrl: string;
  databaseHost: string;
  databasePort: string;
  databaseName: string;
  analyticsUsername: string;
  analyticsPassword: string;
  storeCode: string;
  tablePrefix: string;
  integrationToken: string;
};

const initialForm: FormState = {
  baseUrl: "http://host.docker.internal:8080",
  databaseHost: "host.docker.internal",
  databasePort: "3306",
  databaseName: "magento",
  analyticsUsername: "magento_analytics",
  analyticsPassword: "",
  storeCode: "all",
  tablePrefix: "",
  integrationToken: "",
};

const REPORTING_LOGIN_REQUEST = `Please create a dedicated read-only MariaDB/MySQL login for OpenNeko analytics on our Magento database.

Grant only SELECT and SHOW VIEW on the Magento database. Do not grant INSERT, UPDATE, DELETE, CREATE, ALTER, DROP, ALL PRIVILEGES, or GRANT OPTION.

Please send me the database hostname, port, database name, username, and password, and allow connections from the server running OpenNeko.`;

const CAP_LABELS: Record<string, string> = {
  maxRowsPerChangeset: "Items per change",
  maxPriceDeltaPercent: "Price delta (%)",
  maxDiscountPercent: "Discount (%)",
  maxCouponCount: "Coupons",
  maxProjectedExposure: "Projected exposure",
  maxDailyAutoActions: "Automatic actions/day",
  skuCooldownSeconds: "Time between changes (seconds)",
};

const DOMAIN_LABELS: Record<string, string> = {
  catalog: "Catalog",
  content: "Content",
  customers: "Customers",
  inventory: "Inventory",
  orders: "Orders",
  promotions: "Promotions",
};

function domainLabel(domain: string): string {
  return DOMAIN_LABELS[domain]
    ?? domain.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
}

function visibleCaps(control: StoreControl): Array<[string, number]> {
  const keys = ["maxRowsPerChangeset", "maxDailyAutoActions", "skuCooldownSeconds"];
  if (control.domain === "catalog") keys.push("maxPriceDeltaPercent");
  if (control.domain === "promotions") {
    keys.push("maxDiscountPercent", "maxCouponCount", "maxProjectedExposure");
  }
  return keys.flatMap((key) =>
    typeof control.caps[key] === "number" ? [[key, control.caps[key]] as [string, number]] : [],
  );
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, cache: "no-store" });
  const body = (await response.json().catch(() => null)) as (T & { error?: string }) | null;
  if (!response.ok) throw new Error(body?.error ?? `Request failed (HTTP ${response.status})`);
  return body as T;
}

function healthLabel(status: string): string {
  if (status === "ready" || status === "installed") return "Healthy";
  if (status === "degraded") return "Needs attention";
  if (status === "removed") return "Not installed";
  return status.replaceAll("_", " ");
}

function checkLabel(id: string): string {
  const changeArea = id.match(/^changes-(catalog|content|customers|inventory|orders|promotions)$/)?.[1];
  if (changeArea) return `${domainLabel(changeArea)} changes`;
  if (id === "changes") return "Magento changes";
  if (id === "analytics") return "Reporting access";
  if (id === "magento") return "Magento connection";
  if (id === "graphjin") return "Reporting service";
  if (id === "analytics-query") return "Live reporting check";
  if (id === "bulk-consumers") return "Bulk updates";
  return domainLabel(id.replaceAll("-", " "));
}

function checkDescription(check: DoctorResult["checks"][number]): string {
  const healthy = check.status === "ready";
  if (check.id === "analytics") {
    return healthy
      ? "OpenNeko can read Magento reporting data without permission to change it."
      : "OpenNeko cannot use the read-only Magento reporting login. Check the database connection and permissions.";
  }
  if (check.id === "magento") {
    return healthy
      ? "OpenNeko can reach the Magento store and identify its store configuration."
      : "OpenNeko cannot reach the Magento store. Check the store address and integration token.";
  }
  if (check.id === "graphjin") {
    return healthy
      ? "The Magento reporting service is ready."
      : "The Magento reporting service is unavailable. Check the reporting connection and try again.";
  }
  if (check.id === "analytics-query") {
    return healthy
      ? "A live Magento order query completed successfully."
      : "OpenNeko could not run a live Magento reporting query. Check the reporting login and database.";
  }
  if (check.id === "bulk-consumers") {
    return healthy
      ? "Magento finished its recent bulk updates."
      : "Magento has not completed its recent bulk updates. Check the Magento queue before trying another bulk change.";
  }
  return check.detail;
}

function checkStatusLabel(id: string, status: string): string {
  if (id === "changes" || id.startsWith("changes-")) {
    return status === "ready" ? "Changes available" : "View only";
  }
  if (status === "ready") return "Healthy";
  if (status === "optional") return "Optional";
  if (status === "blocked") return "Needs attention";
  return status.replaceAll("_", " ");
}

function checkTone(status: string): BadgeVariant {
  if (status === "ready") return "success";
  if (status === "optional") return "muted";
  return "danger";
}

function executionModeLabel(mode: string): string {
  if (mode === "approval_required") return "Administrator approval required";
  if (mode === "controlled_automation_eligible") return "Automatic under the configured store limits";
  if (mode === "handoff_only") return "Complete in Magento Admin";
  return mode.replaceAll("_", " ");
}

function dailyLimitLabel(limit: number): string {
  return `Up to ${limit} ${limit === 1 ? "change" : "changes"} per day`;
}

function cooldownLabel(seconds: number): string {
  if (seconds === 0) return "No waiting period";
  if (seconds % 3600 === 0) {
    const hours = seconds / 3600;
    return `At least ${hours} ${hours === 1 ? "hour" : "hours"} between changes`;
  }
  if (seconds % 60 === 0) {
    const minutes = seconds / 60;
    return `At least ${minutes} ${minutes === 1 ? "minute" : "minutes"} between changes`;
  }
  return `At least ${seconds} seconds between changes`;
}

function pausedRuleLabel(reason: string | null): string | null {
  if (!reason) return null;
  if (reason === "suspended_by_admin") return "Paused by an administrator";
  return `Paused: ${reason.replaceAll("_", " ")}`;
}

function activityTone(outcome: ActivityItem["outcome"]): BadgeVariant {
  if (outcome === "completed" || outcome === "reverted") return "success";
  if (outcome === "awaiting_approval" || outcome === "in_progress") return "watch";
  if (outcome === "needs_attention" || outcome === "failed") return "danger";
  return "muted";
}

export type MagentoPackAdminFixture = {
  status: PackStatus;
  doctor: DoctorResult;
  management: StoreManagement;
};

export default function MagentoPackAdmin({ fixture, initialCustomPack, connected }: { fixture?: MagentoPackAdminFixture; initialCustomPack?: string; connected?: string }) {
  const [status, setStatus] = useState<PackStatus | null>(fixture?.status ?? null);
  const [doctor, setDoctor] = useState<DoctorResult | null>(fixture?.doctor ?? null);
  const [management, setManagement] = useState<StoreManagement | null>(fixture?.management ?? null);
  const [loading, setLoading] = useState(!fixture);
  const [busy, setBusy] = useState<string | null>(null);
  const [rotateCredentials, setRotateCredentials] = useState(false);
  const [clearIntegrationToken, setClearIntegrationToken] = useState(false);
  const [form, setForm] = useState<FormState>(initialForm);
  const [rule, setRule] = useState({
    name: "",
    instruction: "",
    domain: "catalog",
    dailyCap: "5",
    cooldownSeconds: "3600",
  });

  const refresh = useCallback(async (runDoctor = false) => {
    setLoading(true);
    try {
      const nextStatus = await api<PackStatus>("/api/admin/packs/magento/status").catch((error) => {
        if (error instanceof Error && error.message.includes("not installed")) return null;
        throw error;
      });
      setStatus(nextStatus);
      if (nextStatus && nextStatus.status !== "removed") {
        const [nextManagement, nextDoctor] = await Promise.all([
          api<StoreManagement>("/api/admin/packs/magento/store-management"),
          runDoctor ? api<DoctorResult>("/api/admin/packs/magento/doctor") : Promise.resolve(null),
        ]);
        setManagement(nextManagement);
        if (nextDoctor) setDoctor(nextDoctor);
      } else if (!nextStatus || nextStatus.status === "removed") {
        setDoctor(null);
        setManagement(null);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (fixture) return;
    const initial = window.setTimeout(() => void refresh(true), 0);
    return () => window.clearTimeout(initial);
  }, [fixture, refresh]);

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((current) => ({ ...current, [key]: value }));
  }

  async function install(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("install");
    try {
      await api<PackStatus>("/api/admin/packs/magento/install", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          inputs: {
            "magento.base_url": form.baseUrl,
            "magento.store_code": form.storeCode || "all",
            "magento.table_prefix": form.tablePrefix,
            "database.connectivity_mode": form.databaseHost === "host.docker.internal" ? "host_gateway" : "remote",
            "database.host": form.databaseHost,
            "database.port": Number(form.databasePort),
            "database.name": form.databaseName,
          },
          secrets: {
            "database.analytics_username": form.analyticsUsername,
            "database.analytics_password": form.analyticsPassword,
            ...(form.integrationToken ? { "magento.integration_token": form.integrationToken } : {}),
          },
        }),
      });
      setForm((current) => ({ ...current, analyticsPassword: "", integrationToken: "" }));
      toast.success("Magento is connected. Metrics are refreshing now.");
      await refresh(true);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function copyReportingLoginRequest() {
    try {
      await navigator.clipboard.writeText(REPORTING_LOGIN_REQUEST);
      toast.success("Request copied. Send it to your Magento host or database administrator.");
    } catch {
      toast.error("Could not copy automatically. Select and copy the request manually.");
    }
  }

  async function runAction(action: "doctor" | "upgrade" | "uninstall") {
    if (action === "uninstall" && !window.confirm("Remove the Magento pack? Historical metrics and operation records will be kept.")) return;
    setBusy(action);
    try {
      if (action === "doctor") {
        setDoctor(await api<DoctorResult>("/api/admin/packs/magento/doctor"));
        toast.success("Connection and permissions checked.");
      } else {
        await api<PackStatus>(`/api/admin/packs/magento/${action}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}",
        });
        toast.success(action === "upgrade" ? "Magento pack updated." : "Magento pack removed safely.");
        await refresh(action === "upgrade");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function saveCredentials(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (form.analyticsPassword && !form.analyticsUsername) {
      toast.error("Enter the reporting username that belongs to this password.");
      return;
    }
    if (!form.analyticsPassword && !form.integrationToken && !clearIntegrationToken) {
      toast.error("Enter a new reporting password, an API token, or choose to remove the API token.");
      return;
    }
    setBusy("configure");
    try {
      const secrets = {
        ...(form.analyticsPassword
          ? {
              "database.analytics_username": form.analyticsUsername,
              "database.analytics_password": form.analyticsPassword,
            }
          : {}),
        ...(form.integrationToken
          ? { "magento.integration_token": form.integrationToken }
          : clearIntegrationToken
            ? { "magento.integration_token": "" }
            : {}),
      };
      await api<PackStatus>("/api/admin/packs/magento/configure", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ secrets }),
      });
      setForm((current) => ({ ...current, analyticsPassword: "", integrationToken: "" }));
      setRotateCredentials(false);
      setClearIntegrationToken(false);
      toast.success("Credentials updated and verified.");
      await refresh(true);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function updateStoreManagement(
    key: string,
    input: Record<string, unknown>,
    success: string,
  ) {
    setBusy(key);
    try {
      const next = await api<StoreManagement>("/api/admin/packs/magento/store-management", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      });
      setManagement(next);
      toast.success(success);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(null);
    }
  }

  async function createRule(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const eligibleDomains = management?.controls.filter(
      (control) => control.enabled && control.autoExecute && control.automationEligible,
    ) ?? [];
    const domain = eligibleDomains.some((control) => control.domain === rule.domain)
      ? rule.domain
      : eligibleDomains[0]?.domain;
    if (!domain) {
      toast.error("Allow routine changes to run automatically in at least one store area first.");
      return;
    }
    await updateStoreManagement(
      "create-rule",
      {
        action: "create_rule",
        name: rule.name,
        instruction: rule.instruction,
        domain,
        actionKind: `magento.manage_${domain}`,
        dailyCap: Number(rule.dailyCap),
        cooldownSeconds: Number(rule.cooldownSeconds),
        enabled: true,
      },
      "Automatic rule saved with its daily limit and waiting period.",
    );
    setRule((current) => ({ ...current, name: "", instruction: "" }));
  }

  const installed = status && status.status !== "removed";
  const visibleRules = management?.rules.filter((item) => !item.isTest) ?? [];

  return (
    <div className="root" style={{ "--page-width": "min(1000px, 100%)" } as React.CSSProperties}>
      <AppHeader back={{ href: "/admin/settings", label: "All settings" }}>
        <SectionNav current="admin" />
      </AppHeader>
      <PageHeading
        title="Packs"
        description="Install and manage packs for your business."
      />

      <CustomPacksAdmin initialPack={initialCustomPack} connected={connected} />

      {loading && !status ? (
        <section className="settings-card"><p className="settings-card-copy">Checking Magento…</p></section>
      ) : installed ? (
        <div className="flex flex-col gap-4">
          <section className="settings-card">
            <div className="settings-card-head">
              <div>
                <h2 className="settings-card-title">Magento pack</h2>
                <p className="settings-card-copy">Version {status.version} · installed {status.installedAt ? <LocalDateTime value={status.installedAt} fallback="recently" /> : "recently"}</p>
              </div>
              <div className="settings-source">
                <strong className={doctor?.status === "blocked" ? "is-warn" : "is-ok"}>{healthLabel(doctor?.status ?? status.status)}</strong>
              </div>
            </div>
            {status.lastError ? <p className="mt-3 text-sm text-danger">{status.lastError}</p> : null}
            <ActionGroup align="start" className="mt-5">
              <Button type="button" disabled={busy !== null} onClick={() => void runAction("doctor")}>{busy === "doctor" ? "Checking…" : "Check health"}</Button>
              <Button type="button" variant="secondary" disabled={busy !== null} onClick={() => void runAction("upgrade")}>{busy === "upgrade" ? "Updating…" : "Update pack"}</Button>
              <Button type="button" variant="secondary" disabled={busy !== null} onClick={() => setRotateCredentials((value) => !value)}>Change credentials</Button>
            </ActionGroup>
          </section>

          <section className="settings-card">
            <div className="settings-card-head">
              <div>
                <h2 className="settings-card-title">Use your Magento pack</h2>
                <p className="settings-card-copy">Everything is installed already. These are the everyday places your team will use.</p>
              </div>
            </div>
            <div className="mt-4 grid gap-3 sm:grid-cols-2">
              {[
                { href: "/", title: "View Magento metrics", copy: "See current store performance and findings." },
                { href: "/workflows", title: "Run automations", copy: "Start or review Magento workflows." },
                { href: "/actions", title: "Review proposed changes", copy: "Approve or reject a specific Magento change prepared by OpenNeko." },
                { href: "/skills", title: "Magento skills", copy: "Choose a focused skill for orders, fulfillment, refunds, inventory, performance, or platform health." },
              ].map((item) => (
                <Link key={item.href} href={item.href} className="rounded-xl border border-border px-4 py-3 transition hover:border-accent">
                  <strong className="font-display text-ui-body font-bold text-text">{item.title}</strong>
                  <p className="mt-1 text-ui-body-sm leading-[1.45] text-text3">{item.copy}</p>
                </Link>
              ))}
            </div>
          </section>

          {management ? (
            <section className="settings-card">
              <div className="settings-card-head">
                <div>
                  <h2 className="settings-card-title">Store change access</h2>
                  <p className="settings-card-copy">Choose which areas can prepare changes. Sensitive changes always require administrator approval, and every write is reconciled against Magento.</p>
                </div>
              </div>

              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                {management.controls.map((control) => (
                  <div key={control.domain} className="rounded-xl border border-border px-4 py-4">
                    <div className="flex items-start justify-between gap-4">
                      <div className="min-w-0 flex-1">
                        <strong className="font-display text-ui-body font-bold text-text">{domainLabel(control.domain)}</strong>
                        <p className="mt-1 text-ui-body-sm text-text3">
                          {control.automationEligible
                            ? "Routine changes in this area can be automated. Higher-risk changes still wait for administrator approval."
                            : "OpenNeko can prepare changes in this area, but an administrator must approve every one."}
                        </p>
                        <p className={`mt-2 font-body text-ui-caption font-semibold ${control.readiness === "ready" && control.enabled ? "text-success-ink" : "text-text3"}`}>
                          {control.readiness !== "ready" || !control.enabled
                            ? control.readinessMessage
                            : control.autoExecute
                              ? "Routine changes can run automatically"
                              : "Every change waits for approval"}
                        </p>
                      </div>
                      <Checkbox
                        label="Enabled"
                        className="shrink-0 whitespace-nowrap text-ui-caption font-semibold"
                        checked={control.enabled}
                        disabled={busy !== null}
                        onCheckedChange={(checked) => void updateStoreManagement(
                          `domain-${control.domain}`,
                          { action: "update_domain", domain: control.domain, enabled: checked === true },
                          `${domainLabel(control.domain)} change access updated.`,
                        )}
                      />
                    </div>
                    <Checkbox
                      label="Allow routine changes to run automatically"
                      className="mt-4"
                      checked={control.autoExecute}
                      disabled={busy !== null || !control.enabled || !control.automationEligible}
                      onCheckedChange={(checked) => void updateStoreManagement(
                        `auto-${control.domain}`,
                        { action: "update_domain", domain: control.domain, autoExecute: checked === true },
                        `${domainLabel(control.domain)} automatic execution updated.`,
                      )}
                    />
                    <p className="mt-2 text-ui-caption leading-[var(--leading-compact)] text-text3">Up to {control.caps.maxRowsPerChangeset ?? 0} items per change; {control.caps.maxDailyAutoActions ?? 0} automatic actions per day.</p>
                    <Disclosure title="Edit limits" className="mt-3">
                      <div className="grid gap-3 sm:grid-cols-2">
                        {visibleCaps(control).map(([key, value]) => (
                          <Field
                            key={`${control.domain}-${key}`}
                            label={CAP_LABELS[key] ?? key}
                            htmlFor={`${control.domain}-${key}`}
                          >
                            <Input
                              id={`${control.domain}-${key}`}
                              key={`${control.domain}-${key}-${value}`}
                              type="number"
                              min="0"
                              defaultValue={value}
                              disabled={busy !== null}
                              onBlur={(event) => {
                                const next = Number(event.target.value);
                                if (Number.isFinite(next) && next >= 0 && next !== value) {
                                  void updateStoreManagement(
                                    `cap-${control.domain}-${key}`,
                                    { action: "update_domain", domain: control.domain, caps: { [key]: next } },
                                    `${domainLabel(control.domain)} limit updated.`,
                                  );
                                }
                              }}
                            />
                          </Field>
                        ))}
                      </div>
                    </Disclosure>
                  </div>
                ))}
              </div>

              <div className="mt-4 rounded-xl border border-danger/30 bg-danger/5 px-4 py-3">
                <strong className="text-sm text-text">Actions OpenNeko will not perform</strong>
                <p className="mt-1 text-ui-body-sm leading-[1.5] text-text2">OpenNeko cannot issue online refunds, approve returns, change financial configuration, or perform money-out operations. It prepares evidence and a Magento Admin handoff only.</p>
              </div>

              <div className="mt-6 border-t border-border pt-5">
                <h3>Automatic rules</h3>
                <p className="mt-1 text-ui-body-sm text-text3">Each rule stops at its daily limit and waits the configured time before changing the same item again.</p>
                {management.controls.some((control) => control.enabled && control.autoExecute && control.automationEligible) ? (
                  <form className="mt-4 grid gap-3 sm:grid-cols-2" onSubmit={createRule}>
                    <Field label="Rule name" htmlFor="magento-rule-name">
                      <Input id="magento-rule-name" required maxLength={120} value={rule.name} onChange={(event) => setRule((current) => ({ ...current, name: event.target.value }))} />
                    </Field>
                    <Field label="Store area" htmlFor="magento-rule-domain">
                      <NativeSelect id="magento-rule-domain" value={rule.domain} onChange={(event) => setRule((current) => ({ ...current, domain: event.target.value }))}>
                        {management.controls.filter((control) => control.enabled && control.autoExecute && control.automationEligible).map((control) => <option key={control.domain} value={control.domain}>{domainLabel(control.domain)}</option>)}
                      </NativeSelect>
                    </Field>
                    <Field label="Plain-language instruction" htmlFor="magento-rule-instruction" className="sm:col-span-2">
                      <Textarea id="magento-rule-instruction" required maxLength={1000} rows={3} value={rule.instruction} onChange={(event) => setRule((current) => ({ ...current, instruction: event.target.value }))} />
                    </Field>
                    <Field label="Daily limit" htmlFor="magento-rule-daily-limit">
                      <Input id="magento-rule-daily-limit" required type="number" min="1" value={rule.dailyCap} onChange={(event) => setRule((current) => ({ ...current, dailyCap: event.target.value }))} />
                    </Field>
                    <Field label="Time between changes (seconds)" htmlFor="magento-rule-cooldown">
                      <Input id="magento-rule-cooldown" required type="number" min="0" value={rule.cooldownSeconds} onChange={(event) => setRule((current) => ({ ...current, cooldownSeconds: event.target.value }))} />
                    </Field>
                    <div className="sm:col-span-2"><Button type="submit" disabled={busy !== null}>{busy === "create-rule" ? "Saving…" : "Save automatic rule"}</Button></div>
                  </form>
                ) : (
                  <p className="mt-3 rounded-lg bg-bg2 px-3 py-3 text-ui-body-sm text-text3">Turn on “Allow routine changes to run automatically” for an eligible area before creating a rule.</p>
                )}
                {visibleRules.length > 0 ? (
                  <ul className="mt-4 flex flex-col gap-2">
                    {visibleRules.map((item) => {
                      const pausedLabel = pausedRuleLabel(item.suspendedReason);
                      return (
                        <li key={item.id} className="flex items-start justify-between gap-3 rounded-xl border border-border px-4 py-3">
                          <div>
                            <strong className="font-display text-ui-body font-bold text-text">{item.name}</strong>
                            <p className="mt-1 text-ui-body-sm text-text3">{item.instruction}</p>
                            <p className="mt-1 text-ui-caption text-text3">{domainLabel(item.domain)} · {dailyLimitLabel(item.dailyCap)} · {cooldownLabel(item.cooldownSeconds)}{pausedLabel ? ` · ${pausedLabel}` : ""}</p>
                          </div>
                          <Button
                            type="button"
                            variant="secondary"
                            disabled={busy !== null}
                            onClick={() => void updateStoreManagement(
                              `rule-${item.id}`,
                              { action: "set_rule_status", ruleId: item.id, enabled: !item.enabled },
                              item.enabled ? "Automatic rule suspended." : "Automatic rule enabled.",
                            )}
                          >{item.enabled ? "Suspend" : "Enable"}</Button>
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
              </div>

              <div className="mt-6 border-t border-border pt-5">
                <h3>Recent activity</h3>
                <p className="mt-1 text-ui-body-sm text-text3">What changed in Magento and whether anything still needs attention.</p>
                {management.activity.filter((item) => !item.isTest).length === 0 ? (
                  <p className="mt-3 text-ui-body-sm text-text3">No store changes yet.</p>
                ) : (
                  <ActivityList items={management.activity.filter((item) => !item.isTest).slice(0, 8)} />
                )}
                {management.activity.some((item) => item.isTest) ? (
                  <Disclosure
                    title="Test activity"
                    meta={`${management.activity.filter((item) => item.isTest).length} hidden`}
                    className="mt-3"
                  >
                    <p className="mb-3 text-ui-body-sm text-text3">Local acceptance checks are kept for audit and hidden from everyday activity.</p>
                    <ActivityList items={management.activity.filter((item) => item.isTest)} />
                  </Disclosure>
                ) : null}
              </div>
            </section>
          ) : null}

          {doctor ? (
            <section className="settings-card">
              <div className="settings-card-head">
                <div>
                  <h2 className="settings-card-title">Health</h2>
                  <p className="settings-card-copy">Plain-language checks for Magento, the reporting connection, and OpenNeko.</p>
                </div>
              </div>
              <ul className="mt-4 flex flex-col gap-3">
                {doctor.checks.map((check) => {
                  const description = checkDescription(check);
                  return (
                    <li key={check.id} className="flex flex-col gap-1 rounded-xl border border-border px-4 py-3 sm:flex-row sm:items-start sm:justify-between">
                      <div className="min-w-0 flex-1">
                        <strong className="font-display text-ui-body font-bold text-text">{checkLabel(check.id)}</strong>
                        <p className="mt-1 text-ui-body-sm leading-[1.45] text-text3">{description}</p>
                        {description !== check.detail ? (
                          <Disclosure title="Technical details" className="mt-2">
                            <p className="break-all text-ui-caption leading-[var(--leading-compact)] text-text3">{check.detail}</p>
                          </Disclosure>
                        ) : null}
                      </div>
                      <Badge variant={checkTone(check.status)} className="mt-1">{checkStatusLabel(check.id, check.status)}</Badge>
                    </li>
                  );
                })}
              </ul>
            </section>
          ) : null}

          {rotateCredentials ? (
            <form className="settings-card" onSubmit={saveCredentials}>
              <div className="settings-card-head">
                <div>
                  <h2 className="settings-card-title">Change credentials</h2>
                  <p className="settings-card-copy">Only enter credentials you want to change. New values are tested before replacing the saved ones.</p>
                </div>
              </div>
              <CredentialFields form={form} update={update} includeToken required={false} />
              <Checkbox
                label="Remove the saved Magento API token"
                className="mt-4"
                checked={clearIntegrationToken}
                disabled={Boolean(form.integrationToken)}
                onCheckedChange={(checked) => setClearIntegrationToken(checked === true)}
              />
              <ActionGroup align="start" className="mt-5">
                <Button type="submit" disabled={busy !== null}>{busy === "configure" ? "Testing and saving…" : "Save credentials"}</Button>
                <Button type="button" variant="secondary" disabled={busy !== null} onClick={() => setRotateCredentials(false)}>Cancel</Button>
              </ActionGroup>
            </form>
          ) : null}

          <section className="settings-card">
            <div className="settings-card-head">
              <div>
                <h2 className="settings-card-title">Remove pack</h2>
                <p className="settings-card-copy">Disables its live configuration and automations. Historical metrics and audit records stay available.</p>
              </div>
              <Button type="button" variant="danger" disabled={busy !== null} onClick={() => void runAction("uninstall")}>{busy === "uninstall" ? "Removing…" : "Remove"}</Button>
            </div>
          </section>
        </div>
      ) : (
        <form className="settings-card" onSubmit={install}>
          <div className="settings-card-head">
            <div>
              <h2 className="settings-card-title">Connect Magento</h2>
              <p className="settings-card-copy">Use a read-only reporting login. OpenNeko discovers the store settings and installs the complete pack automatically.</p>
            </div>
            <div className="settings-source"><strong>About 2 minutes</strong></div>
          </div>

          <Disclosure title="I do not have a read-only reporting login" className="mt-5 bg-bg2">
            <p className="text-ui-body-sm leading-[var(--leading-body)] text-text2">Send this request to your Magento hosting provider or database administrator. OpenNeko never needs your Magento administrator password or Adobe Marketplace keys.</p>
            <pre className="mt-3 whitespace-pre-wrap rounded-lg bg-bg px-3 py-3 text-xs leading-[1.5] text-text2">{REPORTING_LOGIN_REQUEST}</pre>
            <Button type="button" variant="secondary" className="mt-3" onClick={() => void copyReportingLoginRequest()}>Copy request</Button>
          </Disclosure>

          <div className="mt-5 grid gap-4 sm:grid-cols-2">
            <Field
              label="Magento address"
              htmlFor="magento-address"
              hint="The storefront URL as seen from OpenNeko. The default works when Magento is another local Docker or OrbStack stack."
              className="sm:col-span-2"
            >
              <Input id="magento-address" required type="url" value={form.baseUrl} onChange={(event) => update("baseUrl", event.target.value)} />
            </Field>
            <Field label="Database address" htmlFor="magento-database-address">
              <Input id="magento-database-address" required value={form.databaseHost} onChange={(event) => update("databaseHost", event.target.value)} />
            </Field>
            <Field label="Database name" htmlFor="magento-database-name">
              <Input id="magento-database-name" required value={form.databaseName} onChange={(event) => update("databaseName", event.target.value)} />
            </Field>
          </div>

          <CredentialFields form={form} update={update} required />

          <Disclosure title="Advanced settings" className="mt-5">
            <div className="mt-4 grid gap-4 sm:grid-cols-2">
              <Field label="Database port" htmlFor="magento-database-port">
                <Input id="magento-database-port" required type="number" min="1" max="65535" value={form.databasePort} onChange={(event) => update("databasePort", event.target.value)} />
              </Field>
              <Field label="Store code" htmlFor="magento-store-code">
                <Input id="magento-store-code" value={form.storeCode} onChange={(event) => update("storeCode", event.target.value)} />
              </Field>
              <Field label="Table prefix" htmlFor="magento-table-prefix">
                <Input id="magento-table-prefix" value={form.tablePrefix} onChange={(event) => update("tablePrefix", event.target.value)} />
              </Field>
              <Field
                label="Magento API token (optional)"
                htmlFor="magento-api-token"
                hint="This token is only for specific Magento changes that OpenNeko supports. Each change must be enabled by an administrator and still requires approval."
              >
                <Input id="magento-api-token" type="password" autoComplete="off" value={form.integrationToken} onChange={(event) => update("integrationToken", event.target.value)} />
              </Field>
            </div>
          </Disclosure>

          <div className="mt-6 rounded-xl border border-border bg-bg2 px-4 py-3 text-ui-body-sm leading-[1.5] text-text2">
            OpenNeko starts in view-only mode: it can analyze the store but cannot change Magento data. Any supported change is enabled separately by an administrator and requires approval.
          </div>
          <div className="mt-5">
            <Button type="submit" disabled={busy !== null || !form.analyticsPassword}>{busy === "install" ? "Connecting and installing…" : "Connect and install"}</Button>
          </div>
        </form>
      )}
    </div>
  );
}

function ActivityList({ items }: { items: ActivityItem[] }) {
  return (
    <ul className="mt-3 flex flex-col gap-2">
      {items.map((item) => (
        <li key={item.id} className="rounded-inner border border-border px-4 py-3">
          <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <strong className="font-display text-ui-body font-bold text-text">{item.title}</strong>
              <p className="mt-1 text-ui-body-sm leading-[var(--leading-body)] text-text2">{item.description}</p>
              <p className="mt-1.5 text-ui-caption leading-[var(--leading-compact)] text-text3">
                <LocalDateTime value={item.occurredAt} /> · {item.sourceLabel}
                {item.currentState ? ` · ${item.currentState}` : ""}
              </p>
            </div>
            <Badge variant={activityTone(item.outcome)}>{item.outcomeLabel}</Badge>
          </div>
          <Disclosure title="View details" meta={item.technical.area} className="mt-3">
            <dl className="grid gap-x-5 gap-y-3 text-ui-caption sm:grid-cols-2">
              <div>
                <dt className="font-bold text-text2">Requested as</dt>
                <dd className="mt-0.5 text-text3">{item.technical.originalRequest}</dd>
              </div>
              <div>
                <dt className="font-bold text-text2">How it runs</dt>
                <dd className="mt-0.5 text-text3">{executionModeLabel(item.technical.execution)}</dd>
              </div>
              <div>
                <dt className="font-bold text-text2">Operation</dt>
                <dd className="mt-0.5 font-mono text-text3">{item.technical.operation.replaceAll("_", " ")}</dd>
              </div>
              <div>
                <dt className="font-bold text-text2">Audit reference</dt>
                <dd className="mt-0.5 break-all font-mono text-text3">{item.technical.reference}</dd>
              </div>
              {item.technical.inverseOfReference ? (
                <div>
                  <dt className="font-bold text-text2">Restores change</dt>
                  <dd className="mt-0.5 break-all font-mono text-text3">{item.technical.inverseOfReference}</dd>
                </div>
              ) : null}
              {item.technical.bulkReference ? (
                <div>
                  <dt className="font-bold text-text2">Magento job reference</dt>
                  <dd className="mt-0.5 break-all font-mono text-text3">{item.technical.bulkReference}</dd>
                </div>
              ) : null}
            </dl>
          </Disclosure>
        </li>
      ))}
    </ul>
  );
}

function CredentialFields({
  form,
  update,
  includeToken = false,
  required,
}: {
  form: FormState;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  includeToken?: boolean;
  required: boolean;
}) {
  return (
    <div className="mt-4 grid gap-4 sm:grid-cols-2">
      <Field label="Read-only reporting username" htmlFor="magento-reporting-username">
        <Input id="magento-reporting-username" required={required} autoComplete="username" value={form.analyticsUsername} onChange={(event) => update("analyticsUsername", event.target.value)} />
      </Field>
      <Field
        label="Read-only reporting password"
        htmlFor="magento-reporting-password"
        hint={!required ? "Leave blank to keep the saved reporting login." : undefined}
      >
        <Input id="magento-reporting-password" required={required} type="password" autoComplete="new-password" value={form.analyticsPassword} onChange={(event) => update("analyticsPassword", event.target.value)} />
      </Field>
      {includeToken ? (
        <Field
          label="Magento API token (optional)"
          htmlFor="magento-replacement-api-token"
          hint="Leave blank to keep the saved token. A token alone does not allow OpenNeko to change Magento."
          className="sm:col-span-2"
        >
          <Input id="magento-replacement-api-token" type="password" autoComplete="off" value={form.integrationToken} onChange={(event) => update("integrationToken", event.target.value)} />
        </Field>
      ) : null}
    </div>
  );
}
