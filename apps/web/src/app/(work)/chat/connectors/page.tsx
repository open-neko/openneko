"use client";

import { Suspense } from "react";
import IntegrationsList from "@/app/integrations/IntegrationsList";
import { useIntegrationsData } from "@/app/integrations/load";

function ConnectorsLoader() {
  const loaded = useIntegrationsData();
  if (!loaded) return null;
  // Org-wide connections stay in Admin; this screen holds the person's own.
  return (
    <IntegrationsList
      variant="chat"
      initial={{ workspace: [], connectors: loaded.connectors, personal: loaded.personal }}
      isAdmin={false}
      canSetUp={loaded.role === "admin"}
    />
  );
}

export default function ConnectorsPage() {
  return (
    <Suspense fallback={null}>
      <ConnectorsLoader />
    </Suspense>
  );
}
