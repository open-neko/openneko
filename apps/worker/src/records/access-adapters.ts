import { and, app_user, db, eq, sso_group, user_group } from "@neko/db";
import {
  RecordsAccessAdmin,
  type RecordAccessSubject,
} from "@neko/records";
import {
  registerActionAdapter,
  type ActionAdapter,
  type ActionRequestRecord,
} from "@neko/llm/workflows";

export const RECORD_ACCESS_ACTION_KINDS = [
  "app_access_grant",
  "app_access_revoke",
  "app_object_permission_set",
  "app_field_permission_set",
] as const;

export const RECORD_ACCESS_ACTION_DESCRIPTORS = [
  {
    kind: "app_access_grant",
    scope: "internal",
    description: "Grant a specific user or immutable SSO group access to a generated app.",
    default_mode: "ask",
    example: { app: "crm", subject_type: "group", subject_id: "<user_group.id>" },
  },
  {
    kind: "app_access_revoke",
    scope: "internal",
    description: "Revoke a user or SSO group's app access and all child object/field grants.",
    default_mode: "ask",
    example: { app: "crm", subject_type: "user", subject_id: "usr_example" },
  },
  {
    kind: "app_object_permission_set",
    scope: "internal",
    description: "Set read/create/update/delete access for one app object and subject.",
    default_mode: "ask",
    example: {
      app: "crm",
      object: "activity",
      subject_type: "group",
      subject_id: "<user_group.id>",
      read: true,
      create: true,
      update: true,
      delete: false,
    },
  },
  {
    kind: "app_field_permission_set",
    scope: "internal",
    description: "Set read/write access for one app field and subject.",
    default_mode: "ask",
    example: {
      app: "crm",
      object: "opportunity",
      field: "amount",
      subject_type: "group",
      subject_id: "<user_group.id>",
      read: true,
      write: false,
    },
  },
] as const;

type RecordAccessKind = (typeof RECORD_ACCESS_ACTION_KINDS)[number];

function requiredString(payload: Record<string, unknown>, key: string): string {
  const value = payload[key];
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`${key}: a non-empty string is required`);
  }
  return value.trim();
}

function requiredBoolean(payload: Record<string, unknown>, key: string): boolean {
  const value = payload[key];
  if (typeof value !== "boolean") throw new Error(`${key}: a boolean is required`);
  return value;
}

async function liveAdminActor(request: ActionRequestRecord): Promise<string> {
  if (request.actorRole !== "admin") {
    throw new Error("records access actions require an admin actor");
  }
  if (request.actorUserId) {
    const [user] = await db()
      .select({ role: app_user.role, disabledAt: app_user.disabled_at })
      .from(app_user)
      .where(
        and(
          eq(app_user.org_id, request.orgId),
          eq(app_user.id, request.actorUserId),
        ),
      )
      .limit(1);
    if (!user || user.disabledAt || user.role !== "admin") {
      throw new Error("records access admin is disabled or no longer an admin");
    }
    return request.actorUserId;
  }
  const [anyUser] = await db()
    .select({ id: app_user.id })
    .from(app_user)
    .where(eq(app_user.org_id, request.orgId))
    .limit(1);
  if (anyUser) throw new Error("userless access administration is solo-mode only");
  return `urn:openneko:solo-admin:${request.orgId}`;
}

async function accessSubject(
  request: ActionRequestRecord,
): Promise<RecordAccessSubject> {
  const type = requiredString(request.payload, "subject_type");
  const id = requiredString(request.payload, "subject_id");
  if (type === "user") {
    const [user] = await db()
      .select({ id: app_user.id, disabledAt: app_user.disabled_at })
      .from(app_user)
      .where(and(eq(app_user.org_id, request.orgId), eq(app_user.id, id)))
      .limit(1);
    if (!user || user.disabledAt) throw new Error("access subject user was not found or is disabled");
    return { type, id };
  }
  if (type === "group") {
    const [userGroup] = await db()
      .select({ id: user_group.id, slug: user_group.slug })
      .from(user_group)
      .where(and(eq(user_group.org_id, request.orgId), eq(user_group.id, id)))
      .limit(1);
    if (userGroup) return { type, id };
    const [group] = await db()
      .select({ id: sso_group.id })
      .from(sso_group)
      .where(
        and(
          eq(sso_group.org_id, request.orgId),
          eq(sso_group.id, id),
          eq(sso_group.active, true),
        ),
      )
      .limit(1);
    if (!group) throw new Error("access subject group was not found or is inactive");
    return { type, id };
  }
  throw new Error("subject_type must be user or group");
}

function exactKeys(payload: Record<string, unknown>, allowed: readonly string[]): void {
  const expected = new Set(allowed);
  const unknown = Object.keys(payload).filter((key) => !expected.has(key));
  if (unknown.length) throw new Error(`unknown access payload fields: ${unknown.sort().join(", ")}`);
}

export function createRecordAccessActionAdapter(
  kind: RecordAccessKind,
  admin: RecordsAccessAdmin,
): ActionAdapter {
  return async ({ request }) => {
    if (request.kind !== kind) throw new Error(`adapter ${kind} cannot execute ${request.kind}`);
    const [actorUserId, target] = await Promise.all([
      liveAdminActor(request),
      accessSubject(request),
    ]);
    const appId = requiredString(request.payload, "app");
    const actor = { userId: actorUserId, actionRequestId: request.id };
    let result: Record<string, unknown>;
    if (kind === "app_access_grant") {
      exactKeys(request.payload, ["app", "subject_type", "subject_id"]);
      result = await admin.grantApp({ orgId: request.orgId, appId, subject: target, actor });
    } else if (kind === "app_access_revoke") {
      exactKeys(request.payload, ["app", "subject_type", "subject_id"]);
      result = await admin.revokeApp({ orgId: request.orgId, appId, subject: target, actor });
    } else if (kind === "app_object_permission_set") {
      exactKeys(request.payload, [
        "app", "object", "subject_type", "subject_id",
        "read", "create", "update", "delete",
      ]);
      result = await admin.setObject({
        orgId: request.orgId,
        appId,
        objectApiName: requiredString(request.payload, "object"),
        subject: target,
        permissions: {
          canRead: requiredBoolean(request.payload, "read"),
          canCreate: requiredBoolean(request.payload, "create"),
          canUpdate: requiredBoolean(request.payload, "update"),
          canDelete: requiredBoolean(request.payload, "delete"),
        },
        actor,
      });
    } else {
      exactKeys(request.payload, [
        "app", "object", "field", "subject_type", "subject_id", "read", "write",
      ]);
      result = await admin.setField({
        orgId: request.orgId,
        appId,
        objectApiName: requiredString(request.payload, "object"),
        fieldApiName: requiredString(request.payload, "field"),
        subject: target,
        permissions: {
          canRead: requiredBoolean(request.payload, "read"),
          canWrite: requiredBoolean(request.payload, "write"),
        },
        actor,
      });
    }
    return {
      commandOrOperation: kind,
      externalRef: `${appId}:${target.type}:${target.id}`,
      result,
    };
  };
}

export function registerRecordAccessActions(
  admin: RecordsAccessAdmin,
  register: (kind: string, adapter: ActionAdapter) => void = registerActionAdapter,
): void {
  for (const kind of RECORD_ACCESS_ACTION_KINDS) {
    register(kind, createRecordAccessActionAdapter(kind, admin));
  }
}
