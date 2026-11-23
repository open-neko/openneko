"use client";

import { useEffect, useState } from "react";
import AdminOnly from "@/app/admin/AdminOnly";
import { adminApi } from "@/components/admin/admin-api";
import { SkeletonList } from "@/components/ui/skeleton";
import type { SsoSetupStatusResult } from "@/lib/sso-setup";
import SsoSetupForm from "./SsoSetupForm";

type Loaded = { status: SsoSetupStatusResult; workspaceConnected: boolean };
type Integrations = { workspace?: { pluginName: string; connected: boolean }[] };

function notStarted(lastError: string): SsoSetupStatusResult {
  return {
    status: "not_started",
    portalLink: null,
    portalLinkExpiresAt: null,
    provider: null,
    connection: null,
    lastError,
    setupCompletedAt: null,
    environmentId: null,
    organizationId: null,
    environmentTier: null,
    signInConfigured: false,
  };
}

function SsoLoader() {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  useEffect(() => {
    let cancelled = false;
    void Promise.all([
      adminApi<SsoSetupStatusResult>("/api/v1/sso/setup/status"),
      adminApi<Integrations>("/api/v1/integrations/list"),
    ]).then(([status, integrations]) => {
      if (cancelled) return;
      setLoaded({
        status: status.ok ? status.body : notStarted(status.error),
        workspaceConnected:
          integrations.ok &&
          (integrations.body.workspace ?? []).some((p) => p.pluginName === "scalekit" && p.connected),
      });
    });
    return () => {
      cancelled = true;
    };
  }, []);
  if (loaded === null) {
    return (
      <div className="root">
        <SkeletonList />
      </div>
    );
  }
  return <SsoSetupForm initial={loaded.status} loadError={null} workspaceConnected={loaded.workspaceConnected} />;
}

export default function SettingsSsoPage() {
  return (
    <AdminOnly>
      <SsoLoader />
    </AdminOnly>
  );
}
