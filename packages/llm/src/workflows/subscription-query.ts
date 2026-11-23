export type SourceChangeFilter = {
  table: string;
  where?: Record<string, unknown>;
  select?: string[];
  primary_key: string[];
  version_column?: string;
};

/** Validates a SourceChangeFilter's shape; returns null when invalid. */
export function parseSourceChangeFilter(
  filter: Record<string, unknown>,
): SourceChangeFilter | null {
  if (!filter || typeof filter !== "object") return null;
  const f = filter as Partial<SourceChangeFilter>;
  if (typeof f.table !== "string" || f.table.length === 0) return null;
  if (!isIdentifier(f.table)) return null;
  if (!Array.isArray(f.primary_key) || f.primary_key.length === 0) return null;
  if (!f.primary_key.every((c) => typeof c === "string" && isIdentifier(c))) {
    return null;
  }
  if (f.where != null && (typeof f.where !== "object" || Array.isArray(f.where))) {
    return null;
  }
  if (
    f.select != null &&
    (!Array.isArray(f.select) ||
      !f.select.every((c) => typeof c === "string" && isIdentifier(c)))
  ) {
    return null;
  }
  if (
    f.version_column != null &&
    (typeof f.version_column !== "string" || !isIdentifier(f.version_column))
  ) {
    return null;
  }
  return {
    table: f.table,
    where: f.where ?? {},
    select: f.select,
    primary_key: f.primary_key,
    version_column: f.version_column,
  };
}

const IDENT_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

function isIdentifier(s: string): boolean {
  return IDENT_RE.test(s);
}
