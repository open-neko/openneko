"use client";

import { useParams } from "next/navigation";
import type { GroupMemberRow, UserGroupRow } from "@neko/db";
import AdminLoad from "@/app/admin/AdminLoad";
import { AdminShell } from "@/app/admin/AdminShell";
import type { DataAccessRuleView } from "./DataAccessSection";
import { GroupDetailClient, type ItemTypeView } from "./GroupDetailClient";

type GroupResponse = {
  group: UserGroupRow;
  members: GroupMemberRow[];
  grants: Array<{ itemType: string; itemId: string }>;
  rules: DataAccessRuleView[];
  idpRules: Array<{ id: string; idpGroupName: string }>;
};

type UsersResponse = {
  users: Array<{ id: string; email: string; name: string | null; disabled: boolean }>;
};

export default function GroupPage() {
  const { groupId = "" } = useParams<{ groupId: string }>();
  return (
    <AdminLoad
      title="Group"
      paths={[
        `/api/v1/admin/groups/${encodeURIComponent(groupId)}`,
        "/api/v1/admin/users",
        "/api/v1/admin/data-access/settings",
        "/api/v1/admin/items",
      ]}
    >
      {([groupData, usersData, dataAccess, items]) => {
        const { group, members, grants, rules, idpRules } = groupData as GroupResponse;
        const ruleNames = new Map(idpRules.map((r) => [`rule:${r.id}`, r.idpGroupName]));
        const users = (usersData as UsersResponse).users
          .filter((u) => !u.disabled)
          .map(({ id, email, name }) => ({ id, email, name }));
        return (
          <AdminShell
            title={group.name}
            subtitle={group.description ?? (group.kind === "builtin" ? "Built-in group" : "Custom group")}
            back={{ href: "/admin/users?tab=groups", label: "Groups" }}
          >
            <GroupDetailClient
              group={group}
              members={members.map((m) => ({
                ...m,
                sources: m.sources.map((s) => (s === "local" || s === "implicit" ? s : `IdP rule: ${ruleNames.get(s) ?? "removed"}`)),
              }))}
              grants={grants.map((g) => ({ itemType: g.itemType, itemId: g.itemId }))}
              rules={rules}
              users={users}
              itemTypes={(items as { types: ItemTypeView[] }).types}
              groupGrantsEnabled={(dataAccess as { enabled: boolean }).enabled}
            />
          </AdminShell>
        );
      }}
    </AdminLoad>
  );
}
