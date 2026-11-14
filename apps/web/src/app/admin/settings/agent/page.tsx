"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import AgentForm from "./AgentForm";

type Initial = Parameters<typeof AgentForm>[0]["initial"];

export default function SettingsAgentPage() {
  return (
    <AdminLoad title="Agent" paths={["/api/v1/settings/agent", "/api/v1/settings/provider"]}>
      {([agent, providers]) => (
        <AgentForm initial={{ agent, providers } as Initial} />
      )}
    </AdminLoad>
  );
}
