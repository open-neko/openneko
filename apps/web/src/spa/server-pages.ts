// The Go web front keeps a copy (apps/openneko/internal/webfront/front.go);
// its TestServerPagesMatchWeb fails when the two differ.
export const serverPages: string[] = [
  "/chat/connectors",
  "/profile",
  "/admin",
  "/admin/access",
  "/admin/rules",
  "/admin/rules/:policyId",
  "/admin/settings",
  "/admin/settings/agent",
  "/admin/settings/channels",
  "/admin/settings/data",
  "/admin/settings/graphjin",
  "/admin/settings/models",
  "/admin/settings/packs",
  "/admin/settings/packs/magento/operations",
  "/admin/settings/repository",
  "/admin/settings/research",
  "/admin/settings/security",
  "/admin/settings/signin",
  "/admin/settings/spend",
  "/admin/settings/sso",
  "/admin/settings/workflows",
  "/admin/users",
  "/admin/users/groups/:groupId",
  "/device",
  "/integrations",
  "/onboarding",
];

export function serverPagePattern(): RegExp {
  const alternatives = serverPages.map((page) => page.replace(/:[^/]+/g, "[^/]+"));
  return new RegExp(`^(?:${alternatives.join("|")})/?(?:[?#].*)?$`);
}
