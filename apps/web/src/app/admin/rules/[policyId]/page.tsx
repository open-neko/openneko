"use client";

import { useParams } from "next/navigation";
import AdminOnly from "@/app/admin/AdminOnly";
import PolicyDetailClient from "./PolicyDetailClient";

export default function PolicyDetailPage() {
  const { policyId = "" } = useParams<{ policyId: string }>();
  return <AdminOnly><PolicyDetailClient policyId={policyId} /></AdminOnly>;
}
