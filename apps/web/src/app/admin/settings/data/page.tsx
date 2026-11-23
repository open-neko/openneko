"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import DataSourceForm from "./DataSourceForm";

type Initial = Parameters<typeof DataSourceForm>[0]["initial"];

export default function SettingsDataPage() {
  return (
    <AdminLoad title="Data source" paths={["/api/v1/settings/data-source"]}>
      {([initial]) => <DataSourceForm initial={initial as Initial} />}
    </AdminLoad>
  );
}
