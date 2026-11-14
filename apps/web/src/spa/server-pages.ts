// The Go web front keeps a copy (apps/openneko/internal/webfront/front.go);
// its TestServerPagesMatchWeb fails when the two differ.
export const serverPages: string[] = [];

export function serverPagePattern(): RegExp {
  if (serverPages.length === 0) return /(?!)/;
  const alternatives = serverPages.map((page) => page.replace(/:[^/]+/g, "[^/]+"));
  return new RegExp(`^(?:${alternatives.join("|")})/?(?:[?#].*)?$`);
}
