"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import SecurityForm from "./SecurityForm";

type Initial = Parameters<typeof SecurityForm>[0]["initial"];

export default function SettingsSecurityPage() {
  return (
    <AdminLoad title="Security" paths={["/api/v1/settings/install-policy"]}>
      {([initial]) => <SecurityForm initial={initial as Initial} />}
    </AdminLoad>
  );
}
