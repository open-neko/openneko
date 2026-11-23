export const ADMIN_ONLY_SKILLS: ReadonlySet<string> = new Set(["graphjin-config"]);

/**
 * The skill names a run may load. An admin run with the source-config tools
 * keeps its entitlement list; any other run loses the admin-only skills.
 */
export function runSkillScope(
  held: readonly string[] | null | undefined,
  installed: readonly string[],
  admin: boolean,
): string[] | undefined {
  if (admin) return held ? [...held] : undefined;
  return (held ?? installed).filter((name) => !ADMIN_ONLY_SKILLS.has(name));
}
