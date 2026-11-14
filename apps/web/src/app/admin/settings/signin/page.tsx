"use client";

import AdminOnly from "@/app/admin/AdminOnly";
import { AdminShell } from "@/app/admin/AdminShell";
import SignInSetupCard from "./SignInSetupCard";

export default function SettingsSignInPage() {
  return (
    <AdminOnly>
      <AdminShell
        narrow
        title="Email-link sign-in"
        subtitle="Passwordless magic-link sign-in for provisioned users."
        back={{ href: "/admin/settings", label: "Settings" }}
      >
        <SignInSetupCard />
      </AdminShell>
    </AdminOnly>
  );
}
