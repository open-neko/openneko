import type { EvalCase, EvalVariant, SemanticRegistry } from "./schemas";

// Data-path semantics are covered by the variant that exercises the path.
export const DATA_PATH_SEMANTICS: Readonly<Record<string, string>> = {
  "graphjin-direct": "DATA-DIRECT",
  "graphjin-agent": "DATA-DELEGATED",
  "planner-host-execute": "DATA-PLANNER-EXECUTE",
};

export type CoverageConfig = {
  id: string;
  variants: readonly Pick<EvalVariant, "data_path">[];
  cases: readonly Pick<EvalCase, "id" | "semantics" | "assertions">[];
};

export type PrefixCoverage = {
  prefix: string;
  covered: number;
  eval: number;
  declared: number;
  excluded: number;
  total: number;
};

export type SemanticCoverage = {
  covered: ReadonlySet<string>;
  missingEval: string[];
  unknown: string[];
  prefixes: PrefixCoverage[];
};

export function semanticPrefix(id: string): string {
  return id.split("-", 1)[0]!;
}

export function computeSemanticCoverage(
  registry: SemanticRegistry,
  configs: readonly CoverageConfig[],
): SemanticCoverage {
  const known = new Set(registry.entries.map((entry) => entry.id));
  const covered = new Set<string>();
  const unknown = new Set<string>();
  for (const config of configs) {
    for (const variant of config.variants) {
      const semantic = DATA_PATH_SEMANTICS[variant.data_path];
      if (semantic) covered.add(semantic);
    }
    for (const evalCase of config.cases) {
      for (const semantic of evalCase.semantics) covered.add(semantic);
      const cited = [
        ...evalCase.semantics,
        ...evalCase.assertions.flatMap((assertion) => assertion.semantics ?? []),
      ];
      for (const semantic of cited) {
        if (!known.has(semantic)) {
          unknown.add(`${config.id}/${evalCase.id}: ${semantic}`);
        }
      }
    }
  }
  const prefixes = new Map<string, PrefixCoverage>();
  for (const entry of registry.entries) {
    const prefix = semanticPrefix(entry.id);
    const row = prefixes.get(prefix) ?? {
      prefix,
      covered: 0,
      eval: 0,
      declared: 0,
      excluded: 0,
      total: 0,
    };
    row.total += 1;
    if (covered.has(entry.id)) row.covered += 1;
    if (entry.disposition === "eval") row.eval += 1;
    if (entry.disposition === "declared") row.declared += 1;
    if (entry.disposition === "excluded") row.excluded += 1;
    prefixes.set(prefix, row);
  }
  return {
    covered,
    missingEval: registry.entries
      .filter((entry) => entry.disposition === "eval" && !covered.has(entry.id))
      .map((entry) => entry.id),
    unknown: [...unknown].sort(),
    prefixes: [...prefixes.values()].sort((left, right) =>
      left.prefix.localeCompare(right.prefix),
    ),
  };
}

export function formatCoverageTable(rows: readonly PrefixCoverage[]): string {
  const header = ["prefix", "covered", "eval", "declared", "excluded", "total"];
  const body = rows.map((row) => [
    row.prefix,
    ...[row.covered, row.eval, row.declared, row.excluded, row.total].map(String),
  ]);
  const totals = rows.reduce(
    (sum, row) => ({
      covered: sum.covered + row.covered,
      eval: sum.eval + row.eval,
      declared: sum.declared + row.declared,
      excluded: sum.excluded + row.excluded,
      total: sum.total + row.total,
    }),
    { covered: 0, eval: 0, declared: 0, excluded: 0, total: 0 },
  );
  body.push([
    "all",
    ...[totals.covered, totals.eval, totals.declared, totals.excluded, totals.total].map(
      String,
    ),
  ]);
  const widths = header.map((title, column) =>
    Math.max(title.length, ...body.map((cells) => cells[column]!.length)),
  );
  return [header, ...body]
    .map((cells) =>
      cells
        .map((cell, column) =>
          column === 0 ? cell.padEnd(widths[column]!) : cell.padStart(widths[column]!),
        )
        .join("  "),
    )
    .join("\n");
}
