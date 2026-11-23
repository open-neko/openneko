"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import type { DeviceRow, IdpGroupRow, UserGroupRow } from "@neko/db";
import AdminLoad from "@/app/admin/AdminLoad";
import { AdminShell } from "@/app/admin/AdminShell";
import { adminApi } from "@/components/admin/admin-api";
import { useSession } from "@/lib/session-client";
import type { IdpRuleView } from "./IdpRulesPanel";
import { UsersAdminTabs, type UsersAdminTab } from "./UsersAdminTabs";
import type { AdminUserRow } from "./UsersClient";

const TABS: UsersAdminTab[] = ["users", "groups", "idp-rules", "devices"];

type IdpRulesResponse = {
  rules: IdpRuleView[];
  idpGroups: IdpGroupRow[];
  groups: UserGroupRow[];
};

type SigninStatus = {
  provider: { pluginName: string; providerLabel?: string } | null;
  pending: { pluginName: string; providerLabel?: string } | null;
};

type DirectoryStatus = {
  provider?: { pluginName: string; providerLabel: string; canCreateUsers: boolean } | null;
};

function isUnclaimedSoloEmail(email: string): boolean {
  return email.endsWith("@solo.openneko.invalid");
}

export default function AdminUsersPage() {
  return (
    <AdminLoad
      title="User administration"
      paths={[
        "/api/v1/admin/users",
        "/api/v1/admin/idp-rules",
        "/api/v1/admin/devices",
        "/api/v1/admin/signin/status",
      ]}
    >
      {([users, idpRules, devices, signin]) => (
        <UsersAdminView
          users={(users as { users: AdminUserRow[] }).users}
          idpRules={idpRules as IdpRulesResponse}
          devices={(devices as { devices: DeviceRow[] }).devices}
          authGate={signin as SigninStatus}
        />
      )}
    </AdminLoad>
  );
}

function UsersAdminView({
  users,
  idpRules,
  devices,
  authGate,
}: {
  users: AdminUserRow[];
  idpRules: IdpRulesResponse;
  devices: DeviceRow[];
  authGate: SigninStatus;
}) {
  const session = useSession();
  const queryTab = useSearchParams().get("tab") as UsersAdminTab | null;
  const [directory, setDirectory] = useState<DirectoryStatus | null>(null);

  useEffect(() => {
    let cancelled = false;
    void adminApi<DirectoryStatus>("/api/v1/admin/directory").then((result) => {
      if (!cancelled && result.ok) setDirectory(result.body);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const signInLabel = authGate.provider?.providerLabel ?? null;
  const directoryCreateLabel = directory?.provider?.canCreateUsers ? directory.provider.providerLabel : null;
  const identitySetup =
    Boolean(authGate.provider ?? authGate.pending) &&
    users.some((user) => user.id === session?.user?.id && isUnclaimedSoloEmail(user.email));
  const rows: AdminUserRow[] = users.map((user) => ({
    ...user,
    email: isUnclaimedSoloEmail(user.email) ? "Email not set" : user.email,
  }));
  const tab = queryTab && TABS.includes(queryTab) ? queryTab : "users";

  return (
    <AdminShell
      title="User administration"
      subtitle={
        signInLabel
          ? `Multi-user mode via ${signInLabel}.`
          : "Solo mode uses your local admin account."
      }
      back={{ href: "/admin", label: "Admin" }}
    >
      <UsersAdminTabs
        tab={identitySetup ? "users" : tab}
        users={rows}
        identitySetup={identitySetup}
        groups={idpRules.groups}
        idpGroups={idpRules.idpGroups}
        rules={idpRules.rules}
        devices={devices}
        signInProvider={signInLabel ?? authGate.pending?.providerLabel ?? null}
        directoryProvider={directory?.provider?.pluginName ?? null}
        directoryCreateLabel={directoryCreateLabel}
      />
    </AdminShell>
  );
}
