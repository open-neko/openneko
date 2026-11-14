"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import AdminOnly from "@/app/admin/AdminOnly";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import CustomPacksAdmin from "./CustomPacksAdmin";

function PacksPage() {
  const query = useSearchParams();
  const pack = query.get("pack") ?? "";
  const initialPack = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(pack) ? pack : undefined;
  return <div className="root is-narrow">
    <AppHeader back={{ href: "/admin/settings", label: "Settings" }} />
    <PageHeading title="Packs" description="Install included packs, then configure their connections and capabilities." />
    <CustomPacksAdmin initialPack={initialPack} connected={query.get("connected") ?? undefined} />
  </div>;
}

export default function SettingsPacksPage() {
  return <AdminOnly><Suspense fallback={null}><PacksPage /></Suspense></AdminOnly>;
}
