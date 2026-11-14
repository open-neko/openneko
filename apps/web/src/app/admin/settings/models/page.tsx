"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import ModelsForm from "./ModelsForm";

type Initial = Parameters<typeof ModelsForm>[0]["initial"];

export default function SettingsModelsPage() {
  return (
    <AdminLoad title="Models and task routes" paths={["/api/v1/admin/models"]}>
      {([initial]) => <ModelsForm initial={initial as Initial} />}
    </AdminLoad>
  );
}
