"use client";

import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import { confirmDialog } from "@/components/ConfirmModal";
import { Button, ButtonLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Disclosure } from "@/components/ui/Disclosure";
import { Field, Input, NativeSelect } from "@/components/ui/Field";
import { ActionGroup } from "@/components/ui/ActionGroup";

type Provider = { packId: string; connectorId: string; name: string; scopes: string[]; configured: boolean; accounts: { id: string; label: string; status: string }[] };
export default function PackAccounts() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [canConfigure, setCanConfigure] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const alert = useRef<HTMLParagraphElement>(null);
  async function load() {
    try {
      const response = await fetch("/api/pack-accounts", { cache: "no-store" });
      if (!response.ok) throw new Error("Pack accounts could not be loaded. Try again.");
      const data = await response.json(); setError(""); setProviders(data.providers); setCanConfigure(data.canConfigure);
    } catch (error) { setError((error as Error).message); }
    finally { setLoading(false); }
  }
  useEffect(() => { const timer = setTimeout(() => void load(), 0); if (new URLSearchParams(location.search).has("connectionError")) toast.error("Account connection failed. Check the settings and permissions, then try again."); else if (new URLSearchParams(location.search).has("connected")) toast.success("Account connected"); return () => clearTimeout(timer); }, []);
  useEffect(() => { if (error) alert.current?.focus(); }, [error]);
  return <div className="root"><AppHeader /><PageHeading eyebrow="Connections" title="Pack accounts" description="Connect your accounts to installed packs. Each account belongs to the user who connects it." />
    <ButtonLink href="/integrations" variant="ghost">All integrations</ButtonLink>
    {loading ? <p role="status">Loading pack accounts…</p> : null}
    {error ? <><p role="alert" tabIndex={-1} ref={alert}>{error}</p><Button onClick={() => { setLoading(true); void load(); }} disabled={loading}>Try again</Button></> : null}
    {!loading && !error && !providers.length ? <Card><p>No installed packs require an account.</p><ButtonLink href="/admin/settings/packs" variant="secondary">View packs</ButtonLink></Card> : null}
    <div className="grid gap-4 mt-4">{providers.map(provider => <Account key={`${provider.packId}/${provider.connectorId}`} provider={provider} canConfigure={canConfigure} refresh={load} />)}</div>
  </div>;
}
function Account({ provider, canConfigure, refresh }: { provider: Provider; canConfigure: boolean; refresh: () => Promise<void> }) {
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const alert = useRef<HTMLParagraphElement>(null);
  const id = `${provider.packId}-${provider.connectorId}`;
  useEffect(() => { if (error) alert.current?.focus(); }, [error]);
  async function act(action: string, input: Record<string, unknown> = {}) {
    setBusy(true); setError("");
    try {
      const response = await fetch(`/api/pack-accounts/${provider.packId}/${provider.connectorId}/${action}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || "Account request failed");
      if (action === "start") { location.assign(data.authorizationUrl); return; }
      toast.success(action === "disconnect" ? "Account disconnected" : "Client settings saved");
      setSelected(""); await refresh();
    } catch (error) { setError((error as Error).message); }
    finally { setBusy(false); }
  }
  return <Card as="section" aria-label={provider.name} className="grid gap-3">
    <h2 className="font-display text-ui-section font-bold">{provider.name}</h2>
    <Disclosure title="Required permissions"><ul className="text-ui-body-sm break-all">{provider.scopes.map(scope => <li key={scope}>{scope}</li>)}</ul></Disclosure>
    {!provider.configured ? <p>An administrator must save the OAuth client settings before you connect.</p> : null}
    {canConfigure ? <Disclosure title="OAuth client settings"><form className="grid gap-3" onSubmit={event => { event.preventDefault(); const form = event.currentTarget; const data = new FormData(form); void (async () => { if (provider.configured && !await confirmDialog({ title: "Replace client settings?", description: "All users must connect their accounts again.", confirmLabel: "Replace settings" })) return; await act("configure", { clientId: data.get("clientId"), clientSecret: data.get("clientSecret") }); form.reset(); })(); }}>
      <p className="text-ui-body-sm break-all">Callback path: /api/pack-accounts/{provider.packId}/{provider.connectorId}/callback. Use this site&apos;s origin before the path.</p>
      <Field label="Client ID" htmlFor={`${id}-client`}><Input id={`${id}-client`} name="clientId" required disabled={busy} autoComplete="off" /></Field>
      <Field label="Client secret" htmlFor={`${id}-secret`}><Input id={`${id}-secret`} name="clientSecret" type="password" required disabled={busy} autoComplete="new-password" /></Field>
      <Button type="submit" variant="secondary" disabled={busy}>Save client settings</Button>
    </form></Disclosure> : null}
    {provider.accounts.length ? <Field label="Account" htmlFor={`${id}-account`}><NativeSelect id={`${id}-account`} value={selected} disabled={busy} onChange={event => setSelected(event.target.value)}><option value="">Select an account</option>{provider.accounts.map(account => <option key={account.id} value={account.id}>{account.label}{account.status === "reconnect_required" ? " — Reconnect required" : ""}</option>)}</NativeSelect></Field> : <p>No accounts connected.</p>}
    <ActionGroup align="start"><Button variant="primary" disabled={busy || !provider.configured} onClick={() => void act("start")}>{busy ? "Please wait…" : "Connect an account"}</Button>
      <Button variant="secondary" disabled={busy || !selected || !provider.configured} onClick={() => void act("start", { accountId: selected })}>Reconnect</Button>
      <Button variant="danger" disabled={busy || !selected} onClick={() => void (async () => { if (await confirmDialog({ title: "Disconnect this account?", description: "The pack will lose access to this account.", confirmLabel: "Disconnect" })) await act("disconnect", { accountId: selected }); })()}>Disconnect</Button>
    </ActionGroup>
    {error ? <p role="alert" tabIndex={-1} ref={alert}>{error}</p> : null}
  </Card>;
}
