"use client";

import AdminLoad from "@/app/admin/AdminLoad";
import SpendForm from "./SpendForm";

type Initial = Parameters<typeof SpendForm>[0]["initial"];

export default function SettingsSpendPage() {
  return (
    <AdminLoad title="Spending limits" paths={["/api/v1/admin/spend"]}>
      {([initial]) => <SpendForm initial={initial as Initial} />}
    </AdminLoad>
  );
}
