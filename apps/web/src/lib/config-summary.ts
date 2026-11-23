export type SourceSentences = { name: string; kind: string; sentences: string[] };
export type RoleSentence = { name: string; sentence: string };
export type ConfigSentences = { sources: SourceSentences[]; roles: RoleSentence[] };

export type ConfigChangeSummary = {
  before: ConfigSentences;
  after: ConfigSentences;
  yaml: { before: string; after: string };
};

export type ConfigChangeItem = {
  key: string;
  label: string;
  before: string[] | null;
  after: string[] | null;
};

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

export function readConfigSentences(value: unknown): ConfigSentences {
  const r = record(value);
  const sources = (Array.isArray(r?.sources) ? r.sources : []).flatMap((item) => {
    const s = record(item);
    return s && typeof s.name === "string"
      ? [{ name: s.name, kind: typeof s.kind === "string" ? s.kind : "", sentences: strings(s.sentences) }]
      : [];
  });
  const roles = (Array.isArray(r?.roles) ? r.roles : []).flatMap((item) => {
    const role = record(item);
    return role && typeof role.name === "string" && typeof role.sentence === "string"
      ? [{ name: role.name, sentence: role.sentence }]
      : [];
  });
  return { sources, roles };
}

/** The before and after of a source_config_admin proposal, or null. */
export function readConfigChangeSummary(payload: unknown): ConfigChangeSummary | null {
  const summary = record(record(record(payload)?.preview)?.summary);
  if (!summary) return null;
  const yaml = record(summary.yaml);
  return {
    before: readConfigSentences(summary.before),
    after: readConfigSentences(summary.after),
    yaml: {
      before: typeof yaml?.before === "string" ? yaml.before : "",
      after: typeof yaml?.after === "string" ? yaml.after : "",
    },
  };
}

const KIND_LABELS: Record<string, string> = {
  database: "Database",
  api: "API",
  file: "Files",
};

/** One item per touched source or role, with its sentences before and after. */
export function configChangeItems(summary: ConfigChangeSummary): ConfigChangeItem[] {
  const items: ConfigChangeItem[] = [];
  const sourceNames = [
    ...new Set([...summary.before.sources, ...summary.after.sources].map((s) => s.name)),
  ];
  for (const name of sourceNames) {
    const before = summary.before.sources.find((s) => s.name === name) ?? null;
    const after = summary.after.sources.find((s) => s.name === name) ?? null;
    const kind = KIND_LABELS[(after ?? before)?.kind ?? ""] ?? "Source";
    items.push({
      key: `source:${name}`,
      label: `${kind} · ${name}`,
      before: before?.sentences ?? null,
      after: after?.sentences ?? null,
    });
  }
  const roleNames = [
    ...new Set([...summary.before.roles, ...summary.after.roles].map((r) => r.name)),
  ];
  for (const name of roleNames) {
    const before = summary.before.roles.find((r) => r.name === name) ?? null;
    const after = summary.after.roles.find((r) => r.name === name) ?? null;
    items.push({
      key: `role:${name}`,
      label: `Role · ${name}`,
      before: before ? [before.sentence] : null,
      after: after ? [after.sentence] : null,
    });
  }
  return items;
}
