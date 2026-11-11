"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { toast } from "sonner";
import { confirmDialog } from "@/components/ConfirmModal";
import { ActionGroup } from "@/components/ui/action-group";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { Field, Input, NativeSelect } from "@/components/ui/field";
import { LocalDateTime } from "@/components/ui/local-date-time";
import { Badge } from "@/components/ui/badge";
import { newRequestId } from "@/lib/request-id";

type Value = string | number | boolean;
export type PackOperation = "install" | "configure" | "upgrade";
type Source = { id: string; name: string; label: string | null; enabled: boolean; graphqlUrl: string };
type Configuration = { required?: boolean; inputs: Record<string, Value>; dataSourceId?: string; sourceBindings: Record<string, string> };
type Status = { version: string; status: string; lastError: string | null; installedAt: string | null; configuration?: Configuration };
type OAuthStatus = { configured?: boolean; scope?: "user" | "deployment"; key: string; providerLabel: string; clientId: string | null; connected: boolean; account: { id: string; label: string } | null; scopes: string[]; expiresAt: string | null; callbackUrl: string };
type Inspection = {
  source: string; bundleHash: string;
  manifest: {
    management?: { label: string; path: string };
    metadata: { id: string; name: string; version: string; publisher: string };
    inputs: Array<{ key: string; type: string; required?: boolean; default?: Value; description?: string; values?: Value[] }>;
    secrets: Array<{ key: string; purpose: string; required?: boolean }>;
    oauth: Array<{ experience?: { description: string; setupInstructions?: string; helpUrl?: string }; scope?: "user" | "deployment"; key: string; providerLabel: string; clientIdInput: string; clientSecret: string; scopes: string[] }>;
    artifacts: { graphjin?: unknown };
  };
  bindingRequirements: Array<{ key: string; name: string }>;
  permissions: Record<string, string>;
};
type Review = Inspection & { reviewHash: string; inputs: Record<string, Value>; runtime: { source?: { id: string }; bindings: Record<string, string> }; plan: { entries: Array<{ action: string; kind: string; key: string; targetRef: string; reason?: string }> } };
export type PackApplied = { packId: string; name: string; operation: PackOperation; status: string };

export async function packApi<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, body === undefined ? { cache: "no-store" } : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error ?? `Request failed (${response.status})`);
  return result as T;
}

export function installBuiltinPack(packId: string, version?: string): Promise<Status> {
  return packApi<Status>(`/api/admin/packs/${encodeURIComponent(packId)}/install`, { ...(version ? { version } : {}), deferConfiguration: true, idempotencyKey: newRequestId() });
}

function label(key: string): string {
  if (key === "apiWrite") return "API changes";
  if (key === "database") return "Database access";
  const words = key.replaceAll(/[._-]/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export default function PackSetup({ packId, variant = "settings", onChanged, onDirtyChange, onApplied }: {
  packId: string;
  variant?: "settings" | "chat";
  onChanged?: () => void | Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  onApplied?: (result: PackApplied) => void;
}) {
  const [sources, setSources] = useState<Source[]>([]);
  const [detail, setDetail] = useState<Inspection | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [operation, setOperation] = useState<PackOperation>("install");
  const [inputs, setInputs] = useState<Record<string, Value>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [clearedSecrets, setClearedSecrets] = useState<string[]>([]);
  const [bindings, setBindings] = useState<Record<string, string>>({});
  const [sourceId, setSourceId] = useState("");
  const [oauth, setOauth] = useState<Record<string, OAuthStatus>>({});
  const [review, setReview] = useState<{ result: Review; request: Record<string, unknown> } | null>(null);
  const [busy, setBusy] = useState<string | null>("loading");
  const [error, setError] = useState("");
  const [errorDetail, setErrorDetail] = useState("");
  const [missing, setMissing] = useState(false);
  const [dirty, setDirtyState] = useState(false);
  const removeButton = useRef<HTMLButtonElement>(null);
  const errorText = useRef<HTMLParagraphElement>(null);
  const chat = variant === "chat";

  const setDirty = useCallback((value: boolean) => { setDirtyState(value); onDirtyChange?.(value); }, [onDirtyChange]);
  const fail = useCallback((error: unknown, message = "The pack could not be loaded. Try loading it again.") => {
    setError(message);
    setErrorDetail(error instanceof Error ? error.message : String(error));
    requestAnimationFrame(() => errorText.current?.focus());
  }, []);
  function changed() { setReview(null); setDirty(true); setError(""); }
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const load = useCallback(async (mode?: PackOperation, confirmDiscard = true) => {
    const trigger = document.activeElement as HTMLElement | null;
    if (confirmDiscard && dirty && !await confirmDialog({ title: "Discard pack changes?", description: "The current configuration has not been applied.", confirmLabel: "Discard changes" })) { trigger?.focus(); return; }
    setBusy("loading"); setError(""); setMissing(false); setReview(null); setDetail(null); setDirty(false); setSecrets({}); setClearedSecrets([]); setOauth({});
    try {
      const path = `/api/admin/packs/${encodeURIComponent(packId)}`;
      const response = await fetch(`${path}/status`, { cache: "no-store" });
      const current = response.status === 404 ? null : await response.json() as Status;
      if (!response.ok && response.status !== 404) throw new Error("Could not load pack status");
      const action = mode ?? (current?.status === "installed" ? "configure" : "install");
      const inspected = await fetch(`${path}/inspect${action === "configure" && current ? `?version=${encodeURIComponent(current.version)}` : ""}`, { cache: "no-store" });
      if (inspected.status === 404) { setMissing(true); return; }
      const inspection = await inspected.json() as Inspection & { error?: string };
      if (!inspected.ok) throw new Error(inspection.error ?? `Request failed (${inspected.status})`);
      const available = inspection.manifest.artifacts.graphjin
        ? await packApi<{ sources: Source[] }>("/api/settings/data-sources")
        : { sources: [] };
      const usable = available.sources.filter(source => source.enabled && source.graphqlUrl);
      setSources(usable);
      const oauthStatuses = await Promise.all(inspection.manifest.oauth.map(connection =>
        packApi<OAuthStatus>(`/api/pack-accounts/${encodeURIComponent(packId)}/${encodeURIComponent(connection.key)}`),
      ));
      setOauth(Object.fromEntries(oauthStatuses.map(value => [value.key, value])));
      setDetail(inspection); setStatus(current); setOperation(action);
      setInputs(Object.fromEntries(inspection.manifest.inputs.map(input => [
        input.key,
        current?.configuration?.inputs[input.key] ??
          oauthStatuses.find(value => inspection.manifest.oauth.some(connection => connection.key === value.key && connection.clientIdInput === input.key))?.clientId ??
          input.default ??
          (input.type === "boolean" ? false : ""),
      ])));
      setBindings(current?.configuration?.sourceBindings ?? {});
      setSourceId(current?.configuration?.dataSourceId ?? (usable.length === 1 ? usable[0]!.id : ""));
    } catch (error) { fail(error); }
    finally { setBusy(null); }
  }, [dirty, fail, packId, setDirty]);
  const loadRef = useRef(load);
  useEffect(() => { loadRef.current = load; }, [load]);
  useEffect(() => {
    const timer = window.setTimeout(() => void loadRef.current(undefined, false), 0);
    return () => window.clearTimeout(timer);
  }, [packId]);

  async function connectOAuth(connection: Inspection["manifest"]["oauth"][number]) {
    const clientId = String(inputs[connection.clientIdInput] ?? "");
    const clientSecret = secrets[connection.clientSecret] ?? "";
    if (!clientId || (!clientSecret && !oauth[connection.key]?.configured)) {
      setError(`Enter the ${connection.providerLabel} client ID and client secret first.`);
      return;
    }
    setBusy(`oauth-${connection.key}`); setError("");
    try {
      if (connection.scope === "user") {
        const result = await packApi<OAuthStatus>(`/api/pack-accounts/${encodeURIComponent(packId)}/${encodeURIComponent(connection.key)}`, { clientId, clientSecret });
        setOauth(current => ({ ...current, [connection.key]: { ...current[connection.key], ...result } }));
        setSecrets(current => ({ ...current, [connection.clientSecret]: "" }));
        toast.success("Setup saved. Each user can connect on Integrations.");
        setBusy(null);
        return;
      }
      const result = await packApi<{ authorizationUrl: string }>(
        `/api/pack-accounts/${encodeURIComponent(packId)}/${encodeURIComponent(connection.key)}/start`,
        { clientId, clientSecret, returnTo: `/admin/settings/packs` },
      );
      window.location.assign(result.authorizationUrl);
    } catch (error) {
      fail(error, `${connection.providerLabel} could not start account connection.`);
      setBusy(null);
    }
  }

  async function disconnectOAuth(connection: Inspection["manifest"]["oauth"][number]) {
    setBusy(`oauth-${connection.key}`); setError("");
    try {
      const response = await fetch(`/api/pack-accounts/${encodeURIComponent(packId)}/${encodeURIComponent(connection.key)}`, { method: "DELETE" });
      if (!response.ok) throw new Error((await response.json().catch(() => null))?.error ?? `Request failed (${response.status})`);
      setOauth({ ...oauth, [connection.key]: { ...oauth[connection.key]!, connected: false, account: null, expiresAt: null } });
      setReview(null);
      toast.success(`${connection.providerLabel} disconnected.`);
    } catch (error) { fail(error, `${connection.providerLabel} could not be disconnected.`); }
    finally { setBusy(null); }
  }

  async function install() {
    if (!detail) return;
    setBusy("install-builtin"); setError("");
    try {
      await installBuiltinPack(packId, detail.manifest.metadata.version);
      await onChanged?.();
      await load(undefined, false);
    } catch (error) {
      await load(undefined, false);
      fail(error, "The pack needs configuration before it can be installed. Complete the fields below and try again.");
    } finally { setBusy(null); }
  }

  async function prepare(event: FormEvent) {
    event.preventDefault();
    if (!detail) return;
    setBusy("review"); setReview(null); setError("");
    const request = { operation, version: detail.manifest.metadata.version, inputs: Object.fromEntries(Object.entries(inputs).filter(([, value]) => value !== "")), secrets: { ...Object.fromEntries(Object.entries(secrets).filter(([, value]) => value !== "")), ...Object.fromEntries(clearedSecrets.map(key => [key, ""])) }, sourceBindings: bindings, ...(sourceId ? { dataSourceId: sourceId } : {}) };
    try {
      const result = await packApi<Review>(`/api/admin/packs/${encodeURIComponent(packId)}/review`, request);
      setReview({ result, request });
    } catch (error) { fail(error, "The pack could not be reviewed. Check the configuration and try again."); }
    finally { setBusy(null); }
  }

  async function apply() {
    if (!review || !detail) return;
    setBusy(operation); setError("");
    try {
      const result = await packApi<Status>(`/api/admin/packs/${encodeURIComponent(packId)}/${operation}`, { ...review.request, reviewHash: review.result.reviewHash, idempotencyKey: newRequestId() });
      setStatus(result); setReview(null); setSecrets({}); setDirty(false);
      toast.success("Pack changes applied."); await onChanged?.();
      onApplied?.({ packId, name: detail.manifest.metadata.name, operation, status: result.status });
      setOperation("configure");
    } catch (error) {
      setReview(null);
      fail(error, "The pack could not be applied. Share the error details with the pack author, then review the corrected pack again.");
      const response = await fetch(`/api/admin/packs/${encodeURIComponent(packId)}/status`, { cache: "no-store" }).catch(() => null);
      if (response?.ok) setStatus(await response.json() as Status);
      await onChanged?.();
    }
    finally { setBusy(null); }
  }

  async function remove() {
    if (!await confirmDialog({ title: "Remove this pack?", description: "Its automations and connector access will stop. History and business data are retained.", confirmLabel: "Remove pack", destructive: true })) { removeButton.current?.focus(); return; }
    setBusy("uninstall"); setError("");
    try {
      setStatus(await packApi<Status>(`/api/admin/packs/${encodeURIComponent(packId)}/uninstall`, { idempotencyKey: newRequestId() }));
      setReview(null); setSecrets({}); setDirty(false); setOperation("install");
      toast.success("Pack removed. History and data retained."); await onChanged?.();
    } catch (error) { fail(error, "The pack could not be removed. Check its status and try again."); }
    finally { setBusy(null); }
  }

  const errorBlock = <>
    {error ? <p ref={errorText} tabIndex={-1} role="alert" className="break-words text-ui-body-sm text-danger">{error}</p> : null}
    {error && errorDetail ? <Disclosure title="Error details"><p className="break-words text-ui-body-sm">{errorDetail}</p></Disclosure> : null}
  </>;

  if (missing) return <Card as="section" className="grid gap-2" aria-label="Pack setup">
    <p role="alert" className="text-ui-body-sm">OpenNeko has no pack named &quot;{packId}&quot;. Ask to add the source by its connection details.</p>
  </Card>;
  if (!detail) return <Card as="section" className="grid gap-3" aria-label="Pack setup" aria-busy={busy !== null}>
    {busy === "loading" ? <p role="status">Loading pack configuration…</p> : null}
    {errorBlock}
    {error ? <Button variant="secondary" onClick={() => { setError(""); void load(undefined, false); }} disabled={busy !== null}>Retry loading the pack</Button> : null}
  </Card>;

  return <Card as="section" className="grid gap-4" aria-label={chat ? `${detail.manifest.metadata.name} setup` : undefined} aria-busy={busy !== null}>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h2>{detail.manifest.metadata.name}</h2><p className="text-ui-body-sm text-text2">Version {detail.manifest.metadata.version} · Published by {detail.manifest.metadata.publisher}</p>
        {status?.status === "installed" && status.installedAt ? <p className="text-ui-caption text-text2">Installed <LocalDateTime value={status.installedAt} /></p> : null}</div>
      <Badge variant={status?.status === "installed" ? "success" : status?.status === "failed" ? "danger" : "muted"}>{status?.status === "installed" ? "Installed" : status?.status === "failed" ? "Install failed" : "Not installed"}</Badge>
    </div>
    {chat ? errorBlock : null}
    {status?.lastError ? <div className="grid gap-2"><p role="alert" className="text-ui-body-sm text-danger">The last installation attempt failed. The error details say whether to change the pack, its settings or this OpenNeko install. Then review it again.</p><Disclosure title="Error details"><p className="break-words text-ui-body-sm">{status.lastError}</p></Disclosure></div> : null}
    {status?.status === "installed" ? <ActionGroup align="start">
      {!status.configuration?.required && detail.manifest.management && <ButtonLink href={detail.manifest.management.path}>{detail.manifest.management.label}</ButtonLink>}
      <ButtonLink href={`/admin/access?type=pack&id=${encodeURIComponent(packId)}`}>Access</ButtonLink>
      <Button variant="secondary" disabled={busy !== null} onClick={() => void load(operation === "upgrade" ? "configure" : "upgrade")}>{operation === "upgrade" ? "Edit installed version" : "Review available update"}</Button>
      {chat ? null : <Button ref={removeButton} variant="danger" disabled={busy !== null} onClick={() => void remove()}>{busy === "uninstall" ? "Removing…" : "Remove pack"}</Button>}
    </ActionGroup> : null}
    {detail.source !== "embedded" || status?.status === "installed" ? <form onSubmit={prepare} className="grid gap-4">
      {status?.configuration?.required && <p className="text-ui-body-sm text-text2">Installed. Complete and apply the configuration below to activate this pack.</p>}
      <fieldset disabled={busy !== null} className="grid gap-4 sm:grid-cols-2">
        <legend className="mb-3 font-display text-ui-subsection font-bold">{operation === "upgrade" ? "Update configuration" : "Pack configuration"}</legend>
        {detail.manifest.artifacts.graphjin ? <Field label="Data connection" htmlFor={`pack-source-${packId}`} hint="Choose an enabled connection from Data settings.">
          <NativeSelect id={`pack-source-${packId}`} required value={sourceId} onChange={event => { changed(); setSourceId(event.target.value); }}>
            <option value="">Choose a connection</option>
            {sources.map(source => <option key={source.id} value={source.id}>{source.label || source.name || source.id}</option>)}
          </NativeSelect>
        </Field> : null}
        {detail.manifest.inputs.filter(input => status?.status === "installed" || !detail.manifest.oauth.some(connection => connection.scope === "user" && connection.clientIdInput === input.key)).map(input => input.type === "boolean" ? <Checkbox key={input.key} label={label(input.key)} checked={Boolean(inputs[input.key])} onCheckedChange={checked => { changed(); setInputs({ ...inputs, [input.key]: checked === true }); }} /> :
          <Field key={input.key} label={label(input.key)} hint={input.description} htmlFor={`pack-${packId}-${input.key}`}>
            {input.type === "enum" ? <NativeSelect id={`pack-${packId}-${input.key}`} name={input.key} value={String(inputs[input.key] ?? "")} required={input.required} onChange={event => { changed(); setInputs({ ...inputs, [input.key]: input.values?.find(value => String(value) === event.target.value) ?? "" }); }}>
              <option value="">Choose a value</option>{input.values?.map(value => <option key={String(value)} value={String(value)}>{String(value)}</option>)}
            </NativeSelect> : <Input id={`pack-${packId}-${input.key}`} name={input.key} type={input.type === "url" ? "url" : input.type === "integer" ? "number" : "text"} step={input.type === "integer" ? 1 : undefined} required={input.required} value={String(inputs[input.key] ?? "")} onChange={event => { changed(); setInputs({ ...inputs, [input.key]: input.type === "integer" && event.target.value !== "" ? Number(event.target.value) : event.target.value }); }} />}
          </Field>)}
        {(detail.bindingRequirements ?? []).map(binding => <Field key={binding.key} label={`${label(binding.name)} source`} htmlFor={`binding-${packId}-${binding.key}`} hint="Existing read-only source name in this connection."><Input id={`binding-${packId}-${binding.key}`} required value={bindings[binding.key] ?? ""} onChange={event => { changed(); setBindings({ ...bindings, [binding.key]: event.target.value }); }} /></Field>)}
        {detail.manifest.secrets.filter(secret => secret.purpose !== "pack_oauth_token" && (status?.status === "installed" || !detail.manifest.oauth.some(connection => connection.scope === "user" && connection.clientSecret === secret.key))).map(secret => <Field key={secret.key} label={label(secret.key)} htmlFor={`secret-${packId}-${secret.key}`} hint={status?.status === "installed" ? "Leave blank to keep the saved credential." : "Stored securely after installation."}>
          <Input id={`secret-${packId}-${secret.key}`} name={secret.key} type="password" autoComplete="new-password" spellCheck={false} disabled={clearedSecrets.includes(secret.key)} required={secret.required !== false && status?.status !== "installed" && !detail.manifest.oauth.some(connection => connection.clientSecret === secret.key && oauth[connection.key]?.configured)} value={secrets[secret.key] ?? ""} onChange={event => { changed(); setSecrets({ ...secrets, [secret.key]: event.target.value }); }} />
          {status?.status === "installed" && secret.required === false && !detail.manifest.oauth.some(connection => connection.clientSecret === secret.key) ? <Checkbox label="Remove saved credential" checked={clearedSecrets.includes(secret.key)} onCheckedChange={checked => { changed(); setClearedSecrets(current => checked ? [...current, secret.key] : current.filter(key => key !== secret.key)); }} /> : null}
        </Field>)}
      </fieldset>
      {detail.manifest.oauth.filter(connection => connection.scope !== "user" || status?.status === "installed").map(connection => <Card key={connection.key} as="section" className="grid gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div><h3>{connection.providerLabel}</h3><p className="text-ui-body-sm text-text2">{connection.scope === "user" ? "Each user connects their own account on Integrations." : oauth[connection.key]?.account?.label ?? "Connect the account this pack will use."}</p></div>
          <Badge variant={oauth[connection.key]?.connected ? "success" : "muted"}>{connection.scope === "user" ? (oauth[connection.key]?.configured ? "Setup saved" : "Setup required") : oauth[connection.key]?.connected ? "Connected" : "Not connected"}</Badge>
        </div>
        <p className="text-ui-caption text-text2">The account grants {connection.scopes.length} reviewed permissions. Access and refresh tokens are encrypted.</p>
        {oauth[connection.key]?.callbackUrl ? <Disclosure title="Provider setup">{connection.experience?.setupInstructions ? <p className="text-ui-body-sm whitespace-pre-line">{connection.experience.setupInstructions}</p> : null}<p className="text-ui-body-sm">Add this authorized redirect URI to the OAuth client:</p><code className="break-all text-ui-caption">{oauth[connection.key].callbackUrl}</code></Disclosure> : null}
        <ActionGroup align="start">
          <Button type="button" variant="secondary" disabled={busy !== null} onClick={() => void connectOAuth(connection)}>{busy === `oauth-${connection.key}` ? "Saving…" : connection.scope === "user" ? "Save setup" : oauth[connection.key]?.connected ? "Reconnect account" : "Connect account"}</Button>
          {connection.scope !== "user" && oauth[connection.key]?.connected ? <Button type="button" variant="danger" disabled={busy !== null} onClick={() => void disconnectOAuth(connection)}>Disconnect</Button> : null}
        </ActionGroup>
      </Card>)}
      <ActionGroup align="start"><Button type="submit" variant={review ? "secondary" : "primary"} disabled={busy !== null}>{busy === "review" ? "Reviewing…" : "Review changes"}</Button></ActionGroup>
    </form> : chat ? <div className="grid gap-2">
      <p className="text-ui-body-sm text-text2">Install the pack first. Then this card asks for the address and the login.</p>
      <ActionGroup align="start"><Button variant="primary" disabled={busy !== null} onClick={() => void install()}>{busy === "install-builtin" ? "Installing…" : `Install ${detail.manifest.metadata.name}`}</Button></ActionGroup>
    </div> : <p className="text-ui-body-sm text-text2">Install this built-in pack above to enable its configuration.</p>}
    {review ? <section className="grid gap-3" aria-label="Reviewed changes" aria-live="polite">
      <h3>Review before applying</h3>
      <p className="text-ui-body-sm">{review.result.manifest.metadata.name} · Version {review.result.manifest.metadata.version}. Applying these changes enables the pack&apos;s configured automations and reads.</p>
      <p className="text-ui-body-sm">{review.result.runtime.source ? `Connection: ${sources.find(source => source.id === review.result.runtime.source?.id)?.label || sources.find(source => source.id === review.result.runtime.source?.id)?.name || "Selected data connection"}.` : "No data connection is required."} {review.result.plan.entries.filter(entry => entry.action === "create").length} additions, {review.result.plan.entries.filter(entry => entry.action === "update").length} updates, {review.result.plan.entries.filter(entry => entry.action === "retire").length} removals.</p>
      <ul className="grid gap-1 text-ui-body-sm">{Object.entries(review.result.permissions).map(([key, value]) => <li key={key}>{label(key)}: {value}</li>)}</ul>
      <Disclosure title="Configuration and change details"><pre className="overflow-x-auto whitespace-pre-wrap break-all text-ui-caption">{JSON.stringify({ inputs: review.result.inputs, sources: review.result.runtime.bindings, changes: review.result.plan.entries.map(({ action, kind, targetRef, reason }) => ({ action, kind, target: targetRef, reason })), content: review.result.bundleHash }, null, 2)}</pre></Disclosure>
      <Button variant="primary" disabled={busy !== null || review.result.plan.entries.some(entry => entry.action === "conflict")} onClick={() => void apply()}>{busy === operation ? "Applying…" : operation === "install" ? "Approve and install" : "Approve and apply changes"}</Button>
      {review.result.plan.entries.some(entry => entry.action === "conflict") ? <p role="alert">Resolve the conflicting changes, then review again.</p> : null}
    </section> : null}
    {chat ? null : errorBlock}
  </Card>;
}
