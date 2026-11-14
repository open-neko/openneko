"use client";

import AdminOnly from "@/app/admin/AdminOnly";
import RulesClient from "./RulesClient";

export default function PoliciesPage() {
  return <AdminOnly><RulesClient /></AdminOnly>;
}
