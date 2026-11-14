"use client";

import { useEffect, useState } from "react";
import { notFound, useSearchParams } from "next/navigation";
import AdminOnly from "@/app/admin/AdminOnly";
import { AdminShell } from "@/app/admin/AdminShell";
import { ItemAccessPanel } from "@/components/admin/ItemAccessPanel";
import { adminApi } from "@/components/admin/admin-api";

type ItemOption = { id: string; label: string; detail?: string };

const ITEM_TYPE_LABELS: Record<string, string> = {
  skill: "Skill",
  workflow: "Workflow",
  metric: "Metric",
  dashboard: "Dashboard",
  watcher: "Watcher",
  team_memory: "Team memory",
  data_source: "Data source",
  saved_query: "Saved query",
  api_operation: "API operation",
  action: "Action",
  integration: "Integration",
  channel: "Channel",
  pack: "Pack",
};

export default function ItemAccessPage() {
  return (
    <AdminOnly>
      <ItemAccessView />
    </AdminOnly>
  );
}

function ItemAccessView() {
  const searchParams = useSearchParams();
  const type = searchParams.get("type") ?? "";
  const id = searchParams.get("id") ?? "";
  const typeLabel = ITEM_TYPE_LABELS[type];
  if (!typeLabel || !id) notFound();

  const [option, setOption] = useState<ItemOption | null>(null);
  useEffect(() => {
    let cancelled = false;
    void adminApi<{ items: ItemOption[] }>(`/api/v1/admin/items?type=${encodeURIComponent(type)}`).then((result) => {
      if (cancelled) return;
      if (result.ok) {
        setOption(result.body.items.find((item) => item.id === id) ?? null);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [type, id]);

  const label = option?.label ?? id;
  return (
    <AdminShell
      title={`${typeLabel}: ${label}`}
      subtitle={option?.detail ?? "Choose which groups hold this item."}
      back={{ href: "/admin/users?tab=groups", label: "Groups" }}
    >
      <ItemAccessPanel itemType={type} itemId={id} label={label} />
    </AdminShell>
  );
}
