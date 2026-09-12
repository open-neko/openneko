import { connection } from "next/server";
import { app_user, asc, db, eq, isUnclaimedSoloEmail } from "@neko/db";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";
import { getPluginStatus } from "@/lib/auth";
import { AdminDenied, AdminShell } from "../AdminShell";
import { UsersClient, type AdminUserRow } from "./UsersClient";

export default async function AdminUsersPage() {
  await connection();
  const actor = await getCurrentActor();
  if (actor.role !== "admin") return <AdminDenied />;

  const orgId = await getOrgId();
  const [users, pluginStatus] = await Promise.all([
    db()
      .select({
        id: app_user.id,
        sub: app_user.sub,
        email: app_user.email,
        name: app_user.name,
        role: app_user.role,
        disabledAt: app_user.disabled_at,
        createdAt: app_user.created_at,
        lastLoginAt: app_user.last_login_at,
      })
      .from(app_user)
      .where(eq(app_user.org_id, orgId))
      .orderBy(asc(app_user.email)),
    getPluginStatus(),
  ]);

  const identitySetup = users.some((user) => user.id === actor.userId && isUnclaimedSoloEmail(user.email));
  const rows: AdminUserRow[] = users.map((user) => ({
    id: user.id,
    email: isUnclaimedSoloEmail(user.email) ? "Email not set" : user.email,
    name: user.name,
    role: user.role,
    disabled: Boolean(user.disabledAt),
    hasSignedIn: Boolean(user.sub ?? user.lastLoginAt),
    lastLoginAt: user.lastLoginAt?.toISOString() ?? null,
    createdAt: user.createdAt?.toISOString() ?? null,
  }));

  return (
    <AdminShell
      title="User administration"
      subtitle={
        pluginStatus.authProvider
          ? `Multi-user mode via ${pluginStatus.authProvider}.`
          : "Solo mode uses your local admin account."
      }
      back={{ href: "/admin", label: "Admin" }}
      wide
    >
      <section className="settings-card">
        <div className="settings-card-head">
          <div>
            <h2 className="settings-card-title">{identitySetup ? "Your admin email" : "Users"}</h2>
            <p className="settings-card-copy">
              {identitySetup
                ? "Your local account is ready to use. Add your email before enabling SSO; your work stays with this account."
                : "Provision users before their first sign-in, assign roles, and disable accounts. Providers without automatic provisioning (e.g. magic link) only ever sign in users listed here."}
            </p>
          </div>
          <div className="settings-source">
            <strong className="is-ok">{rows.length} total</strong>
          </div>
        </div>

        <UsersClient users={rows} identitySetup={identitySetup} />
      </section>
    </AdminShell>
  );
}
