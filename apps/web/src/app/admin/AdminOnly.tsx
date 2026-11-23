"use client";

import type { ReactNode } from "react";
import { useSession } from "@/lib/session-client";
import { AdminDenied } from "./AdminShell";

export default function AdminOnly({ children }: { children: ReactNode }) {
  const session = useSession();
  if (session === undefined) return null;
  if (session?.role !== "admin") return <AdminDenied />;
  return <>{children}</>;
}
