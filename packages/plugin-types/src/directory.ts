import { z } from "zod";

/**
 * Directory contract. A plugin that declares `directory` supplies the
 * users, groups and group memberships of the connected identity provider.
 * OpenNeko mirrors them and applies its IdP group rules; the provider
 * decides memberships, OpenNeko decides what each group may use.
 */

export const DirectoryUser = z.object({
  /** Stable provider user id, used only to join memberships in one snapshot. */
  externalId: z.string().min(1),
  /** OIDC subject when the provider knows it; sign-in matches on it. */
  sub: z.string().min(1).nullable().optional(),
  email: z.string().min(3),
  name: z.string().nullable().optional(),
  active: z.boolean().default(true),
});
export type DirectoryUser = z.infer<typeof DirectoryUser>;

export const DirectoryGroup = z.object({
  /** Immutable provider group id; the same id sign-in claims carry. */
  externalId: z.string().min(1),
  name: z.string().nullable().optional(),
});
export type DirectoryGroup = z.infer<typeof DirectoryGroup>;

export const DirectoryMembership = z.object({
  userExternalId: z.string().min(1),
  groupExternalId: z.string().min(1),
});
export type DirectoryMembership = z.infer<typeof DirectoryMembership>;

export const ListDirectoryParams = z.object({
  cursor: z.string().min(1).nullable().optional(),
});
export type ListDirectoryParams = z.infer<typeof ListDirectoryParams>;

export const ListDirectoryResult = z.object({
  /** Provider tenant, e.g. the Scalekit organization id. Must match sign-in `orgId`. */
  tenantId: z.string().min(1),
  users: z.array(DirectoryUser).default([]),
  groups: z.array(DirectoryGroup).default([]),
  memberships: z.array(DirectoryMembership).default([]),
  /** Present when more pages follow. */
  nextCursor: z.string().min(1).nullable().optional(),
});
export type ListDirectoryResult = z.infer<typeof ListDirectoryResult>;

export const DirectoryChange = z.discriminatedUnion("op", [
  z.object({
    op: z.literal("create_user"),
    email: z.string().min(3),
    name: z.string().nullable().optional(),
  }),
  z.object({
    op: z.literal("deactivate_user"),
    externalId: z.string().min(1),
  }),
]);
export type DirectoryChange = z.infer<typeof DirectoryChange>;

export const ApplyDirectoryChangeParams = z.object({ change: DirectoryChange });
export type ApplyDirectoryChangeParams = z.infer<typeof ApplyDirectoryChangeParams>;

export const ApplyDirectoryChangeResult = z.object({
  user: DirectoryUser.nullable().optional(),
});
export type ApplyDirectoryChangeResult = z.infer<typeof ApplyDirectoryChangeResult>;

/**
 * Directory capability declaration. Singleton: one installed plugin may
 * declare it. `read` flags say what `list_directory` returns; `write`
 * flags say which `apply_directory_change` ops the plugin accepts.
 */
export const DirectoryCapabilityDeclaration = z.object({
  providerLabel: z.string().min(1),
  read: z
    .object({
      users: z.boolean().default(true),
      groups: z.boolean().default(true),
      memberships: z.boolean().default(true),
    })
    .default({ users: true, groups: true, memberships: true }),
  write: z
    .object({
      createUser: z.boolean().default(false),
      deactivateUser: z.boolean().default(false),
    })
    .default({ createUser: false, deactivateUser: false }),
});
export type DirectoryCapabilityDeclaration = z.infer<typeof DirectoryCapabilityDeclaration>;

export function directoryChangeAllowed(
  declaration: DirectoryCapabilityDeclaration,
  change: DirectoryChange,
): boolean {
  return change.op === "create_user" ? declaration.write.createUser : declaration.write.deactivateUser;
}
