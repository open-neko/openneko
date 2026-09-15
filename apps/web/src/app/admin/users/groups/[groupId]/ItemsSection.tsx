"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { adminApi } from "@/components/admin/admin-api";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Disclosure } from "@/components/ui/disclosure";
import { NativeSelect } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import type { ItemTypeView } from "./GroupDetailClient";

type ItemOption = { id: string; label: string; detail?: string };

function ItemTypeGrants({
  groupId,
  type,
  grants,
  onError,
}: {
  groupId: string;
  type: ItemTypeView;
  grants: string[];
  onError: (message: string | null) => void;
}) {
  const router = useRouter();
  const [options, setOptions] = useState<ItemOption[] | null>(null);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const all = grants.includes("*");
  const specific = grants.filter((id) => id !== "*");
  const labels = new Map((options ?? []).map((o) => [o.id, o]));
  const available = (options ?? []).filter((o) => !grants.includes(o.id));

  async function load() {
    if (options) return;
    const result = await adminApi<{ items: ItemOption[] }>(`/api/admin/items?type=${type.type}`);
    const items = result.ok ? result.body.items : [];
    setOptions(items);
    setSelected(items.find((o) => !grants.includes(o.id))?.id ?? "");
  }

  async function change(itemId: string, grant: boolean) {
    setBusy(true);
    onError(null);
    const result = await adminApi("/api/admin/item-grants", grant ? "POST" : "DELETE", { groupId, itemType: type.type, itemId });
    setBusy(false);
    if (!result.ok) return onError(result.error);
    router.refresh();
  }

  return (
    <Disclosure
      title={type.plural}
      meta={all ? "All" : specific.length ? `${specific.length} granted` : "None"}
      onToggle={(event) => {
        if ((event.currentTarget as HTMLDetailsElement).open) void load();
      }}
    >
      <div className="flex flex-col gap-3 border-t border-border px-3.5 py-3 text-sm">
        <Checkbox
          label={`All current and future ${type.plural.toLowerCase()}`}
          checked={all}
          disabled={busy}
          onCheckedChange={(checked) => void change("*", checked === true)}
        />
        {specific.length > 0 && (
          <ul className="flex flex-col gap-2">
            {specific.map((id) => (
              <li key={id} className="flex flex-wrap items-center justify-between gap-2">
                <span>
                  <Link className="font-semibold text-text hover:text-accent" href={`/admin/access?type=${type.type}&id=${encodeURIComponent(id)}`}>
                    {labels.get(id)?.label ?? id}
                  </Link>
                  {labels.get(id)?.detail ? <span className="block text-xs text-text3">{labels.get(id)?.detail}</span> : null}
                </span>
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => void change(id, false)}>Remove</Button>
              </li>
            ))}
          </ul>
        )}
        {options === null ? (
          <Spinner />
        ) : available.length === 0 ? (
          <p className="text-text3">{options.length === 0 ? `No ${type.plural.toLowerCase()} exist yet.` : `Every ${type.label.toLowerCase()} is granted.`}</p>
        ) : (
          <div className="grid grid-cols-[minmax(200px,1fr)_auto] items-end gap-3 max-[520px]:grid-cols-1">
            <NativeSelect aria-label={`Choose a ${type.label.toLowerCase()}`} value={selected} onChange={(e) => setSelected(e.target.value)}>
              {available.map((o) => (
                <option key={o.id} value={o.id}>{o.detail ? `${o.label} · ${o.detail}` : o.label}</option>
              ))}
            </NativeSelect>
            <Button disabled={busy || !selected} onClick={() => void change(selected, true)}>Grant</Button>
          </div>
        )}
      </div>
    </Disclosure>
  );
}

export function ItemsSection({
  groupId,
  grants,
  itemTypes,
  onError,
}: {
  groupId: string;
  grants: Array<{ itemType: string; itemId: string }>;
  itemTypes: ItemTypeView[];
  onError: (message: string | null) => void;
}) {
  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <div>
          <h2 className="settings-card-title">Items</h2>
          <p className="settings-card-copy">
            Members can see and use each item this group holds, and OpenNeko uses those items in their agent runs. A pack grant covers every item the pack installs.
          </p>
        </div>
      </div>
      <div className="flex flex-col gap-2">
        {itemTypes.map((type) => (
          <ItemTypeGrants
            key={type.type}
            groupId={groupId}
            type={type}
            grants={grants.filter((g) => g.itemType === type.type).map((g) => g.itemId)}
            onError={onError}
          />
        ))}
      </div>
    </section>
  );
}
