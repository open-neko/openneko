"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import IntegrationsList from "./IntegrationsList";
import { useIntegrationsData } from "./load";
import { personalConnectionsFixture, pluginConnectionsFixture } from "./visual-fixture";

function visualTestEnabled(): boolean {
  return (
    process.env.NODE_ENV !== "production" &&
    (process.env.OPENNEKO_VISUAL_TEST === "true" ||
      process.env.NEXT_PUBLIC_VISUAL_TEST === "true")
  );
}

function LiveIntegrations() {
  const loaded = useIntegrationsData();
  if (!loaded) return null;
  return (
    <IntegrationsList
      key="live"
      initial={{
        workspace: loaded.workspace,
        connectors: loaded.connectors,
        personal: loaded.personal,
      }}
      isAdmin={loaded.role === "admin"}
    />
  );
}

export function IntegrationsView() {
  const state = useSearchParams().get("state") ?? "";
  if (visualTestEnabled() && state === "plugins") {
    return <IntegrationsList key="preview-plugins" preview initial={pluginConnectionsFixture} />;
  }
  if (visualTestEnabled() && ["connected", "unconfigured", "disconnected"].includes(state)) {
    return (
      <IntegrationsList
        key={`preview-${state}`}
        preview
        initial={{ workspace: [], connectors: [], personal: personalConnectionsFixture(state) }}
        isAdmin={false}
      />
    );
  }
  return <LiveIntegrations />;
}

export default function IntegrationsPage() {
  return (
    <Suspense fallback={null}>
      <IntegrationsView />
    </Suspense>
  );
}
