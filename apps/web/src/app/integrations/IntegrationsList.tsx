"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { toast } from "sonner";
import { Plug } from "lucide-react";
import type { PackUserConnectionStatus } from "@neko/llm/graphjin/pack-user-connections";
import { confirmDialog } from "@/components/ConfirmModal";
import { ActionGroup } from "@/components/ui/action-group";
import { Card, CardHeader, CardContent, CardFooter } from "@/components/ui/card";
import { OverflowMenu, MenuItem } from "@/components/ui/overflow-menu";
import { Disclosure } from "@/components/ui/disclosure";
import { Badge } from "@/components/ui/badge";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import { Button, ButtonLink } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty";
import { SearchInput } from "@/components/ui/search-input";
import { matchesListSearch } from "@/lib/list-search";

type Row = {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
  scopes: string[];
  flow: string;
  credentialScope: string;
  connected: boolean;
  connectedAt: string | null;
};

type InitialState = { workspace: Row[]; connectors: Row[]; personal?: PackUserConnectionStatus[] };

export default function IntegrationsList({ initial, isAdmin = true, preview = false }: { initial: InitialState; isAdmin?: boolean; preview?: boolean }) {
  const [personal, setPersonal] = useState(initial.personal ?? []);
  const [workspace, setWorkspace] = useState<Row[]>(initial.workspace);
  const [connectors, setConnectors] = useState<Row[]>(initial.connectors);
  const [busy, setBusy] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const params = useSearchParams();
  const visiblePersonal = personal.filter(row => matchesListSearch(query, row.providerLabel, row.packId, row.accountLabel ?? "", ...row.scopes));
  const visibleWorkspace = workspace.filter((row) =>
    matchesListSearch(
      query,
      row.pluginId,
      row.pluginName,
      row.providerLabel,
      row.flow,
      row.credentialScope,
      ...row.scopes,
    ),
  );
  const visibleConnectors = connectors.filter((row) =>
    matchesListSearch(
      query,
      row.pluginId,
      row.pluginName,
      row.providerLabel,
      row.flow,
      row.credentialScope,
      ...row.scopes,
    ),
  );

  useEffect(() => {
    const err = params.get("error");
    if (err) toast.error(err);
    const ok = params.get("connected");
    if (ok) toast.success(`Connected ${ok}`);
  }, [params]);

  async function disconnect(pluginName: string, isDeployment: boolean) {
    setBusy(pluginName);
    try {
      const res = await fetch(
        `/api/integrations/disconnect/${encodeURIComponent(pluginName)}`,
        { method: "POST" },
      );
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error ?? `HTTP ${res.status}`);
      }
      const update = (prev: Row[]) =>
        prev.map((r) =>
          r.pluginName === pluginName
            ? { ...r, connected: false, connectedAt: null }
            : r,
        );
      if (isDeployment) setWorkspace(update);
      else setConnectors(update);
      toast.success(`Disconnected ${pluginName}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function managePersonal(row: PackUserConnectionStatus, disconnect = false) {
    if (preview) return;
    if (disconnect && !await confirmDialog({ title: `Disconnect ${row.providerLabel}?`, description: "Your automations will lose access. Pending approvals using this connection will need to be requested again.", confirmLabel: "Disconnect", destructive: true })) return;
    setBusy(`${row.packId}:${row.key}`);
    try {
      const response = await fetch(`/api/my/pack-accounts/${encodeURIComponent(row.packId)}/${encodeURIComponent(row.key)}`, { method: disconnect ? "DELETE" : "POST" });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error ?? "Connection failed");
      if (!disconnect) { window.location.assign(result.authorizationUrl); return; }
      setPersonal(rows => rows.map(value => value.packId === row.packId && value.key === row.key ? { ...value, connected: false, accountLabel: null } : value));
      toast.success(`Disconnected ${row.providerLabel}`);
    } catch (error) { toast.error(error instanceof Error ? error.message : "Connection failed"); }
    finally { setBusy(null); }
  }

  function RowView({
    row,
    isDeployment,
  }: {
    row: Row;
    isDeployment: boolean;
  }) {
    return (
      <li
        key={row.pluginName}
        className="flex items-center gap-4 p-4 rounded-xl border border-border bg-bg max-[480px]:items-stretch max-[480px]:flex-col"
      >
        <div className="flex-1 min-w-0">
          <div className="font-display text-ui-subsection font-bold text-text">{row.providerLabel}</div>
          <div className="text-ui-caption text-text3 truncate">
            {row.pluginName}
          </div>
          <div className="text-ui-caption text-text3 mt-1 truncate">
            Scopes: {row.scopes.join(", ")}
          </div>
          {row.connected && row.connectedAt && (
            <div className="text-ui-caption text-text2 mt-1">
              Connected {new Date(row.connectedAt).toLocaleString()}
            </div>
          )}
        </div>
        {row.connected ? (
          <Button
            variant="secondary"
            disabled={busy === row.pluginName}
            onClick={() => disconnect(row.pluginName, isDeployment)}
          >
            {busy === row.pluginName ? "…" : "Disconnect"}
          </Button>
        ) : (
          <ButtonLink
            href={`/api/integrations/connect/${encodeURIComponent(row.pluginName)}/start`}
          >
            Connect
          </ButtonLink>
        )}
      </li>
    );
  }

  return (
    <div className="root">
      <AppHeader />
      <PageHeading
        title="Integrations"
        description="Connect external accounts for agent actions. Credentials remain in this deployment."
      />

      {preview && <p role="status" className="mt-4 text-ui-body-sm text-text2">Visual preview. Account actions are disabled.</p>}

      {workspace.length + connectors.length + personal.length > 0 ? (
        <div className="mt-6 max-w-[520px]">
          <SearchInput
            label="Search integrations"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search integrations and scopes"
          />
        </div>
      ) : null}

      {visiblePersonal.length > 0 && <section aria-labelledby="my-connections">
        <h2 id="my-connections" className="mt-6 mb-2 font-display text-ui-section font-bold text-text">My connections</h2>
        <p className="text-ui-body-sm text-text3 mb-3">Connect your own accounts for your work and automations. Other users connect separately.</p>
        <ul className="flex flex-col gap-3">{visiblePersonal.map(row => <Card as="li" key={`${row.packId}:${row.key}`} className="grid gap-4" aria-busy={busy === `${row.packId}:${row.key}`}>
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="font-display text-ui-subsection font-bold text-text">{row.providerLabel}</h3>
              <Badge variant={row.connected ? "success" : "muted"}>{row.connected ? "Connected" : "Not connected"}</Badge>
            </div>
            <p className="text-ui-body-sm text-text2 break-words">{row.accountLabel ?? (row.configured ? "Connect your account to get started." : "An admin needs to finish setup first.")}</p>
          </CardHeader>
          {row.experience?.description && <CardContent><p className="text-ui-body-sm text-text2">{row.experience.description}</p></CardContent>}
          <CardFooter>
            <ActionGroup align="start">
              <Button size="md" variant={row.connected ? "secondary" : "primary"} disabled={preview || busy !== null || !row.configured} onClick={() => void managePersonal(row)}>{busy === `${row.packId}:${row.key}` ? "Working…" : row.connected ? "Reconnect" : "Connect account"}</Button>
              {row.connected && <OverflowMenu size="icon" label={`More actions for ${row.providerLabel}`} align="start"><MenuItem danger disabled={preview || busy !== null} onSelect={() => void managePersonal(row, true)}>Disconnect</MenuItem></OverflowMenu>}
            </ActionGroup>
          </CardFooter>
          <Disclosure title="Permissions and help"><div className="grid min-w-0 gap-4"><ul className="min-w-0 text-ui-caption text-text2 break-all">{row.scopes.map(scope => <li key={scope}>{scope}</li>)}</ul>{row.experience?.helpUrl && <ButtonLink size="md" className="justify-self-start" href={row.experience.helpUrl} target="_blank" rel="noreferrer">Connection help</ButtonLink>}</div></Disclosure>
        </Card>)}</ul>
      </section>}

      {visibleWorkspace.length > 0 && (
        <>
          <h2 className="mt-6 mb-2 font-display text-ui-section font-bold text-text">
            Workspace connections
          </h2>
          <p className="text-ui-body-sm text-text3 mb-2">
            One org-wide authorization, consented once by an admin and shared
            by every operator. Authorizing opens a browser consent screen that
            names the scopes and the endpoint.
          </p>
          <ul className="flex flex-col gap-3">
            {visibleWorkspace.map((row) => (
              <RowView key={row.pluginName} row={row} isDeployment />
            ))}
          </ul>
        </>
      )}

      {visibleConnectors.length > 0 && (
        <>
          <h2 className="mt-6 mb-2 font-display text-ui-section font-bold text-text">
            Per-operator connectors
          </h2>
          <p className="text-ui-body-sm text-text3 mb-2">
            Each operator authorizes independently with their own account.
          </p>
          <ul className="flex flex-col gap-3">
            {visibleConnectors.map((row) => (
              <RowView key={row.pluginName} row={row} isDeployment={false} />
            ))}
          </ul>
        </>
      )}

      {workspace.length === 0 && connectors.length === 0 && personal.length === 0 && (
        <EmptyState
          className="py-20"
          icon={<Plug aria-hidden="true" />}
          title="No integrations available"
          body={isAdmin ? "Install a pack or plugin that provides a connection, then return here to connect your account." : "An admin needs to install a pack before you can connect your account."}
          action={isAdmin ? <ButtonLink href="/admin/settings/packs">Review packs</ButtonLink> : undefined}
        />
      )}

      {query && visibleWorkspace.length === 0 && visibleConnectors.length === 0 && visiblePersonal.length === 0 ? (
        <EmptyState
          className="py-20"
          icon={<Plug aria-hidden="true" />}
          title="No matching integrations"
          body="Try another provider, plugin, or scope."
        />
      ) : null}
    </div>
  );
}
