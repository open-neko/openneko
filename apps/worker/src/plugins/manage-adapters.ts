import { execFile } from "node:child_process";
import {
  OFFICIAL_MARKETPLACE_NAME,
  OFFICIAL_MARKETPLACE_URL,
  readManifest,
  removeEntry,
  runInstall,
  writeManifest,
} from "@open-neko/plugin-install";
import { randomUUID } from "node:crypto";
import { registerActionAdapter } from "@neko/llm/workflows";

/**
 * ADM3 — executes approved plugin_install / plugin_uninstall action
 * requests. The chat agent can only PROPOSE these (policy-gated); the
 * worker executes after approval, reusing the CLI's install machinery
 * (marketplace pin + integrity + install-policy snapshot). Required env
 * keys are never prompted through this path — a missing key fails the
 * action with a pointer to the secrets flow.
 */
export function registerPluginManagementAdapters(opts: {
  repoRoot: string;
  getInstallPolicy: () => Promise<{
    allowUnverified: boolean;
    allowGitUrlInstalls: boolean;
    allowSandboxedSkillEscape: boolean;
    allowedMarketplaces: string[];
  }>;
}): void {
  registerActionAdapter("plugin_install", async ({ request }) => {
    const spec = String(
      (request.payload as Record<string, unknown>).spec ?? "",
    ).trim();
    if (!spec) throw new Error("plugin_install: payload.spec is required");
    const policy = await opts.getInstallPolicy();
    const result = await runInstall({
      spec,
      repoRoot: opts.repoRoot,
      trustedMarketplaces: [
        { name: OFFICIAL_MARKETPLACE_NAME, url: OFFICIAL_MARKETPLACE_URL },
      ],
      npmRunner: (args, cwd) =>
        new Promise<void>((resolve, reject) => {
          execFile("npm", args, { cwd, maxBuffer: 32 * 1024 * 1024 }, (err) =>
            err ? reject(err) : resolve(),
          );
        }),
      envPrompt: async (plugin, requirement) => {
        throw new Error(
          `${plugin} requires ${requirement.key}. Set it first (openneko secrets set ${plugin} ${requirement.key} …) and re-approve the install — credentials never flow through chat.`,
        );
      },
      policySnapshot: policy,
    });
    return {
      commandOrOperation: `install ${spec}`,
      result: {
        name: result.name,
        version: result.version,
        source: result.source,
        network: result.permissions.network,
        envAlreadySet: result.envAlreadySet,
      },
    };
  });

  registerActionAdapter("plugin_uninstall", async ({ request }) => {
    const name = String(
      (request.payload as Record<string, unknown>).name ?? "",
    ).trim();
    if (!name) throw new Error("plugin_uninstall: payload.name is required");
    const manifest = await readManifest(opts.repoRoot);
    if (!manifest || !manifest.plugins.some((p) => p.name === name)) {
      throw new Error(`plugin_uninstall: ${name} is not installed`);
    }
    await writeManifest(opts.repoRoot, removeEntry(manifest, name));
    return {
      commandOrOperation: `uninstall ${name}`,
      result: { name, removed: true },
    };
  });
}

/**
 * ADM1 — executes approved user_admin action requests (admin-approved
 * per the user_management_default policy; K2 enforces the approver).
 * invite pre-creates the app_user row so SSO links by email on first
 * sign-in; deactivate sets disabled_at — sign-in and live sessions both
 * die on it.
 */
export function registerUserAdminAdapter(): void {
  registerActionAdapter("user_admin", async ({ request }) => {
    const { app_user, and, db, eq } = await import("@neko/db");
    const payload = request.payload as Record<string, unknown>;
    const action = String(payload.action ?? "");
    const orgId = request.orgId;

    if (action === "invite") {
      const email = String(payload.email ?? "").trim().toLowerCase();
      const role = payload.role === "admin" ? "admin" : "member";
      if (!email) throw new Error("user_admin invite: email required");
      const [existing] = await db()
        .select({ id: app_user.id })
        .from(app_user)
        .where(and(eq(app_user.org_id, orgId), eq(app_user.email, email)))
        .limit(1);
      if (existing) {
        return {
          commandOrOperation: `invite ${email}`,
          result: { userId: existing.id, alreadyExisted: true },
        };
      }
      const id = randomUUID();
      await db().insert(app_user).values({ id, email, org_id: orgId, role });
      return {
        commandOrOperation: `invite ${email} as ${role}`,
        result: { userId: id, role },
      };
    }

    const userId = String(payload.userId ?? "");
    if (!userId) throw new Error(`user_admin ${action}: userId required`);
    const where = and(eq(app_user.org_id, orgId), eq(app_user.id, userId));

    if (action === "set_role") {
      const role = payload.role === "admin" ? "admin" : "member";
      const rows = await db()
        .update(app_user)
        .set({ role, updated_at: new Date() })
        .where(where)
        .returning({ id: app_user.id });
      if (rows.length === 0) throw new Error(`user ${userId} not found`);
      return { commandOrOperation: `set_role ${userId} ${role}`, result: { userId, role } };
    }
    if (action === "deactivate" || action === "reactivate") {
      const disabled_at = action === "deactivate" ? new Date() : null;
      const rows = await db()
        .update(app_user)
        .set({ disabled_at, updated_at: new Date() })
        .where(where)
        .returning({ id: app_user.id });
      if (rows.length === 0) throw new Error(`user ${userId} not found`);
      return {
        commandOrOperation: `${action} ${userId}`,
        result: { userId, disabled: action === "deactivate" },
      };
    }
    throw new Error(`user_admin: unknown action "${action}"`);
  });
}

/**
 * ADM5 — executes approved channel_admin action requests (admin-approved
 * per the channel_management_default policy). Same verbs as the
 * admin-map API: link an identity to an app_user, unlink it back to
 * anonymous, block/unblock it.
 */
export function registerChannelAdminAdapter(): void {
  registerActionAdapter("channel_admin", async ({ request }) => {
    const { and, app_user, channel_identity, db, eq, isNull } = await import(
      "@neko/db"
    );
    const payload = request.payload as Record<string, unknown>;
    const action = String(payload.action ?? "");
    const identityId = String(payload.identityId ?? "");
    const orgId = request.orgId;
    if (!identityId) throw new Error(`channel_admin ${action}: identityId required`);
    const where = and(
      eq(channel_identity.org_id, orgId),
      eq(channel_identity.id, identityId),
    );
    const now = new Date();

    if (action === "link") {
      const appUserId = String(payload.appUserId ?? "");
      const [user] = await db()
        .select({ id: app_user.id })
        .from(app_user)
        .where(
          and(
            eq(app_user.org_id, orgId),
            eq(app_user.id, appUserId),
            isNull(app_user.disabled_at),
          ),
        )
        .limit(1);
      if (!user) throw new Error(`channel_admin link: unknown or disabled user ${appUserId}`);
      const rows = await db()
        .update(channel_identity)
        .set({
          app_user_id: user.id,
          status: "linked",
          verified_at: now,
          updated_at: now,
        })
        .where(where)
        .returning({ id: channel_identity.id });
      if (rows.length === 0) throw new Error(`channel identity ${identityId} not found`);
      return {
        commandOrOperation: `link ${identityId} → ${appUserId}`,
        result: { identityId, appUserId, status: "linked" },
      };
    }

    if (action === "unlink" || action === "block" || action === "unblock") {
      const rows = await db()
        .update(channel_identity)
        .set({
          ...(action === "block"
            ? { status: "blocked" }
            : { app_user_id: null, status: "unverified", verified_at: null }),
          updated_at: now,
        })
        .where(where)
        .returning({ id: channel_identity.id, status: channel_identity.status });
      if (rows.length === 0) throw new Error(`channel identity ${identityId} not found`);
      return {
        commandOrOperation: `${action} ${identityId}`,
        result: { identityId, status: rows[0].status },
      };
    }

    throw new Error(`channel_admin: unknown action "${action}"`);
  });
}
