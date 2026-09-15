"use client";

import { useCallback, useEffect, useState } from "react";
import { adminApi } from "@/components/admin/admin-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/field";

type Grant = { groupId: string; name: string; legacy: boolean };
type Group = { id: string; name: string; slug: string };

export function RecordAppAccessPanel({ appId }: { appId: string }) {
  const [grants, setGrants] = useState<Grant[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const load = useCallback(async () => {
    const [current, all] = await Promise.all([
      adminApi<{ grants: Grant[] }>(`/api/a/${encodeURIComponent(appId)}/admin/access`),
      adminApi<{ groups: Group[] }>("/api/admin/groups"),
    ]);
    if (current.ok) setGrants(current.body.grants);
    if (all.ok) {
      const held = new Set(current.ok ? current.body.grants.map((g) => g.groupId) : []);
      const candidates = all.body.groups.filter((g) => g.slug !== "administrators" && !held.has(g.id));
      setGroups(candidates);
      setSelected(candidates[0]?.id ?? "");
    }
  }, [appId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function change(groupId: string, grant: boolean) {
    setBusy(true);
    setMessage(null);
    const result = await adminApi(`/api/a/${encodeURIComponent(appId)}/admin/access`, "POST", { groupId, grant });
    setBusy(false);
    setMessage(result.ok ? "The change is queued. It appears here when it finishes." : result.error);
    window.setTimeout(() => void load(), 1500);
  }

  return (
    <section className="records-admin-panel">
      <header>
        <div>
          <h2>Group access</h2>
          <p>Groups that can open this app. Object and field permissions below still set what each group can do.</p>
        </div>
      </header>
      {message ? <p className="text-sm text-text2" role="status">{message}</p> : null}
      <ul className="my-3 flex flex-col gap-2 text-sm">
        {grants.length === 0 ? <li className="text-text3">No group has access. Administrators always have access.</li> : null}
        {grants.map((grant) => (
          <li key={grant.groupId} className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-2">
              <span className="font-semibold text-text">{grant.name}</span>
              {grant.legacy ? <Badge variant="muted">IdP group</Badge> : null}
            </span>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void change(grant.groupId, false)}>Revoke</Button>
          </li>
        ))}
      </ul>
      {groups.length > 0 && (
        <div className="grid grid-cols-[minmax(200px,1fr)_auto] items-end gap-3 max-[520px]:grid-cols-1">
          <NativeSelect aria-label="Group" value={selected} onChange={(e) => setSelected(e.target.value)}>
            {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </NativeSelect>
          <Button disabled={busy || !selected} onClick={() => void change(selected, true)}>Grant access</Button>
        </div>
      )}
    </section>
  );
}
