"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import ResearchForm from "./ResearchForm";

type Initial = Parameters<typeof ResearchForm>[0]["initial"];

export default function SettingsResearchPage() {
  return (
    <AdminLoad title="Industry research" paths={["/api/v1/settings/provider"]}>
      {([initial]) => <ResearchForm initial={initial as Initial} />}
    </AdminLoad>
  );
}
