import { readFile } from "node:fs/promises";
import { dirname, isAbsolute, resolve } from "node:path";
import { parse } from "yaml";
import { contentDigest, textDigest } from "./canonical";
import {
  CaseSchema,
  DatasetSchema,
  EvalConfigSchema,
  MetricQuestionInputSchema,
  PricingCatalogSchema,
  SemanticRegistrySchema,
  SuiteSchema,
  ThresholdPolicySchema,
  type EvalCase,
  type EvalConfig,
  type PricingCatalog,
  type EvalDataset,
  type EvalSuite,
  type EvalThresholdPolicy,
  type EvalVariant,
  type SemanticRegistry,
} from "./schemas";

const SECRET_SHAPE =
  /(?:\bsk-[A-Za-z0-9_-]{12,}\b|\bAIza[0-9A-Za-z_-]{20,}\b|\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bglpat-[A-Za-z0-9_-]{12,}\b|\bAKIA[0-9A-Z]{16}\b|\bxox[baprs]-[A-Za-z0-9-]{12,}\b|\bBearer\s+[A-Za-z0-9._~+/=-]{8,}\b|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----)/u;
const CREDENTIAL_KEY =
  /^(?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password|authorization)$/iu;

function rejectLiteralCredentials(value: unknown, path: string): void {
  if (Array.isArray(value)) {
    value.forEach((item, index) => rejectLiteralCredentials(item, `${path}[${index}]`));
    return;
  }
  if (!value || typeof value !== "object") return;
  for (const [key, nested] of Object.entries(value as Record<string, unknown>)) {
    if (
      CREDENTIAL_KEY.test(key) &&
      typeof nested === "string" &&
      !/^env:[A-Z][A-Z0-9_]*$/u.test(nested)
    ) {
      throw new Error(`${path}.${key}: literal credentials are forbidden; use env:NAME refs`);
    }
    rejectLiteralCredentials(nested, `${path}.${key}`);
  }
}

export type LoadedCase = EvalCase & {
  sourcePath: string;
  oraclePath?: string;
  oraclePaths?: Readonly<Record<string, string>>;
  contentId: string;
};

export type LoadedOracleSpec = NonNullable<EvalCase["oracle"]> & {
  path?: string;
};

/**
 * The oracle spec governing one phase of a case: the phase-keyed bundle
 * entry when the case declares `oracles`, otherwise the whole-episode
 * `oracle`. Returns undefined when a bundle case has no entry for the
 * phase (a phase that only carries method/behavior assertions).
 */
export function oracleSpecForPhase(
  evalCase: LoadedCase,
  phase: string,
): LoadedOracleSpec | undefined {
  if (evalCase.oracles) {
    const spec = evalCase.oracles[phase];
    if (!spec) return undefined;
    const path = evalCase.oraclePaths?.[phase];
    return { ...spec, ...(path ? { path } : {}) };
  }
  if (!evalCase.oracle) return undefined;
  return {
    ...evalCase.oracle,
    ...(evalCase.oraclePath ? { path: evalCase.oraclePath } : {}),
  };
}

export type LoadedEval = {
  configPath: string;
  config: EvalConfig;
  suite: EvalSuite;
  thresholdPolicy?: EvalThresholdPolicy;
  datasets: ReadonlyMap<string, EvalDataset>;
  datasetPaths: ReadonlyMap<string, string>;
  datasetSnapshots: ReadonlyMap<string, string>;
  cases: readonly LoadedCase[];
  semantics?: SemanticRegistry;
  pricing?: PricingCatalog;
  digests: {
    config: string;
    suite: string;
    datasets: string;
    cases: string;
    semantics?: string;
    pricing?: string;
    thresholdPolicy?: string;
  };
};

function resolveRef(ownerPath: string, ref: string): string {
  return isAbsolute(ref) ? ref : resolve(dirname(ownerPath), ref);
}

async function readYaml(path: string): Promise<unknown> {
  const source = await readFile(path, "utf8");
  if (SECRET_SHAPE.test(source)) {
    throw new Error(`${path}: contains a literal secret-shaped value; use env:NAME refs`);
  }
  let value: unknown;
  try {
    value = parse(source);
  } catch (cause) {
    throw new Error(`${path}: invalid YAML`, { cause });
  }
  rejectLiteralCredentials(value, path);
  return value;
}

function mergeRecord(
  parent: Record<string, unknown>,
  child: Record<string, unknown>,
): Record<string, unknown> {
  const result: Record<string, unknown> = { ...parent };
  for (const [key, value] of Object.entries(child)) {
    if (key === "extends") continue;
    const inherited = result[key];
    result[key] =
      inherited &&
      value &&
      typeof inherited === "object" &&
      typeof value === "object" &&
      !Array.isArray(inherited) &&
      !Array.isArray(value)
        ? mergeRecord(
            inherited as Record<string, unknown>,
            value as Record<string, unknown>,
          )
        : value;
  }
  return result;
}

function expandVariants(raw: unknown): unknown {
  if (!raw || typeof raw !== "object") return raw;
  const document = raw as Record<string, unknown>;
  if (!Array.isArray(document.variants)) return raw;
  const byId = new Map<string, Record<string, unknown>>();
  for (const candidate of document.variants) {
    if (candidate && typeof candidate === "object") {
      const item = candidate as Record<string, unknown>;
      if (typeof item.id === "string") byId.set(item.id, item);
    }
  }
  const visiting = new Set<string>();
  const resolved = new Map<string, Record<string, unknown>>();
  const visit = (id: string): Record<string, unknown> => {
    const cached = resolved.get(id);
    if (cached) return cached;
    const current = byId.get(id);
    if (!current) throw new Error(`variant ${id} does not exist`);
    if (visiting.has(id)) throw new Error(`variant inheritance cycle at ${id}`);
    visiting.add(id);
    const parentId = current.extends;
    const value =
      typeof parentId === "string"
        ? mergeRecord(visit(parentId), current)
        : mergeRecord({}, current);
    visiting.delete(id);
    resolved.set(id, value);
    return value;
  };
  return {
    ...document,
    variants: document.variants.map((candidate) => {
      const id = (candidate as { id?: unknown } | undefined)?.id;
      return typeof id === "string" ? visit(id) : candidate;
    }),
  };
}

function ensureUnique(values: readonly string[], label: string): void {
  const duplicates = values.filter((value, index) => values.indexOf(value) !== index);
  if (duplicates.length) {
    throw new Error(`${label} contains duplicate IDs: ${[...new Set(duplicates)].join(", ")}`);
  }
}

export async function loadEval(configPathInput: string): Promise<LoadedEval> {
  const configPath = resolve(configPathInput);
  const rawConfig = expandVariants(await readYaml(configPath));
  const config = EvalConfigSchema.parse(rawConfig);
  ensureUnique(config.variants.map((variant) => variant.id), "config variants");

  let pricing: PricingCatalog | undefined;
  let pricingDigest: string | undefined;
  if (config.pricing) {
    const pricingPath = resolveRef(configPath, config.pricing.ref);
    pricing = PricingCatalogSchema.parse(await readYaml(pricingPath));
    ensureUnique(
      pricing.entries.map((entry) => `${entry.provider}/${entry.model}`),
      "pricing catalog entries",
    );
    pricingDigest = contentDigest(pricing);
  }

  const suitePath = resolveRef(configPath, config.suite.ref);
  const suite = SuiteSchema.parse(await readYaml(suitePath));
  let thresholdPolicy: EvalThresholdPolicy | undefined;
  let thresholdPolicyDigest: string | undefined;
  if (suite.threshold_policy) {
    const policyPath = resolveRef(suitePath, suite.threshold_policy.ref);
    thresholdPolicy = ThresholdPolicySchema.parse(await readYaml(policyPath));
    ensureUnique(
      thresholdPolicy.gates.map((gate) => gate.id),
      "threshold policy gates",
    );
    ensureUnique(
      thresholdPolicy.capabilities.map((capability) => capability.id),
      "threshold policy capabilities",
    );
    const declaredCapabilities = new Set(
      thresholdPolicy.capabilities.map((capability) => capability.id),
    );
    const undeclared = thresholdPolicy.gates
      .flatMap((gate) => (gate.capability ? [gate.capability] : []))
      .filter((capability) => !declaredCapabilities.has(capability));
    if (undeclared.length) {
      throw new Error(
        `threshold policy gates reference undeclared capabilities: ${[
          ...new Set(undeclared),
        ].join(", ")}`,
      );
    }
    thresholdPolicyDigest = contentDigest(thresholdPolicy);
  }
  const selected = config.suite.cases ? new Set(config.suite.cases) : undefined;

  const cases: LoadedCase[] = [];
  for (const caseRef of suite.cases) {
    const casePath = resolveRef(suitePath, caseRef.ref);
    const parsedCase = CaseSchema.parse(await readYaml(casePath));
    if (caseRef.assertion_attribution) {
      const knownAssertions = new Set(
        parsedCase.assertions.map((assertion) => assertion.id),
      );
      const unknownAssertions = Object.keys(
        caseRef.assertion_attribution,
      ).filter((id) => !knownAssertions.has(id));
      if (unknownAssertions.length) {
        throw new Error(
          `${suitePath}: assertion attribution for ${parsedCase.id} references unknown assertions: ${unknownAssertions.join(", ")}`,
        );
      }
      parsedCase.assertions = parsedCase.assertions.map((assertion) => {
        const attribution = caseRef.assertion_attribution?.[assertion.id];
        return attribution
          ? {
              ...assertion,
              capabilities: attribution.capabilities,
              ...(attribution.semantics
                ? { semantics: attribution.semantics }
                : {}),
            }
          : assertion;
      });
    }
    if (parsedCase.product_path === "metric") {
      parsedCase.input = MetricQuestionInputSchema.parse(parsedCase.input);
    }
    if (selected && !selected.has(parsedCase.id)) continue;
    const oraclePath = parsedCase.oracle?.ref
      ? resolveRef(casePath, parsedCase.oracle.ref)
      : undefined;
    const oracleDigest = oraclePath
      ? textDigest(await readFile(oraclePath, "utf8"))
      : null;
    let oraclePaths: Record<string, string> | undefined;
    let oracleDigests: Record<string, string> | undefined;
    for (const [phase, spec] of Object.entries(parsedCase.oracles ?? {})) {
      if (!spec.ref) continue;
      const path = resolveRef(casePath, spec.ref);
      (oraclePaths ??= {})[phase] = path;
      (oracleDigests ??= {})[phase] = textDigest(await readFile(path, "utf8"));
    }
    cases.push({
      ...parsedCase,
      sourcePath: casePath,
      ...(oraclePath ? { oraclePath } : {}),
      ...(oraclePaths ? { oraclePaths } : {}),
      contentId: contentDigest({
        case: parsedCase,
        oracleDigest,
        ...(oracleDigests ? { oracleDigests } : {}),
      }),
    });
  }
  ensureUnique(cases.map((item) => item.id), "suite cases");
  if (thresholdPolicy) {
    const attributedCapabilities = new Set(
      cases.flatMap((evalCase) =>
        evalCase.assertions.flatMap(
          (assertion) => assertion.capabilities ?? [],
        ),
      ),
    );
    const declaredCapabilities = new Set(
      thresholdPolicy.capabilities.map((capability) => capability.id),
    );
    const undeclared = [...attributedCapabilities].filter(
      (capability) => !declaredCapabilities.has(capability),
    );
    if (undeclared.length) {
      throw new Error(
        `assertion attribution references capabilities absent from the threshold policy: ${undeclared.sort().join(", ")}`,
      );
    }
    const uncovered = thresholdPolicy.gates
      .flatMap((gate) => (gate.capability ? [gate.capability] : []))
      .filter((capability) => !attributedCapabilities.has(capability));
    if (uncovered.length) {
      throw new Error(
        `threshold policy capabilities have no assertion attribution: ${[
          ...new Set(uncovered),
        ].join(", ")}`,
      );
    }
  }
  if (selected) {
    const missing = [...selected].filter((id) => !cases.some((item) => item.id === id));
    if (missing.length) throw new Error(`selected cases not in suite: ${missing.join(", ")}`);
  }

  const datasets = new Map<string, EvalDataset>();
  const datasetPaths = new Map<string, string>();
  const datasetSnapshots = new Map<string, string>();
  for (const datasetRef of config.datasets) {
    const datasetPath = resolveRef(configPath, datasetRef.ref);
    const dataset = DatasetSchema.parse(await readYaml(datasetPath));
    if (!dataset.snapshots.some((snapshot) => snapshot.id === datasetRef.snapshot)) {
      throw new Error(
        `${datasetPath}: snapshot ${datasetRef.snapshot} is not declared by ${dataset.id}`,
      );
    }
    if (datasets.has(dataset.id)) throw new Error(`duplicate dataset ${dataset.id}`);
    datasets.set(dataset.id, dataset);
    datasetPaths.set(dataset.id, datasetPath);
    datasetSnapshots.set(dataset.id, datasetRef.snapshot);
  }
  const missingDatasets = cases
    .map((item) => item.dataset)
    .filter((id) => !datasets.has(id));
  if (missingDatasets.length) {
    throw new Error(`cases reference unconfigured datasets: ${[...new Set(missingDatasets)].join(", ")}`);
  }

  let semantics: SemanticRegistry | undefined;
  let semanticsDigest: string | undefined;
  if (config.semantics) {
    const semanticsPath = resolveRef(configPath, config.semantics.ref);
    semantics = SemanticRegistrySchema.parse(await readYaml(semanticsPath));
    ensureUnique(semantics.entries.map((entry) => entry.id), "semantic registry");
    const known = new Set(semantics.entries.map((entry) => entry.id));
    const unknown = cases.flatMap((evalCase) =>
      [
        ...evalCase.semantics,
        ...evalCase.assertions.flatMap((assertion) => assertion.semantics ?? []),
      ]
        .filter((id) => !known.has(id))
        .map((id) => `${evalCase.id}:${id}`),
    );
    if (unknown.length) {
      throw new Error(`cases reference unknown semantics: ${unknown.join(", ")}`);
    }
    semanticsDigest = contentDigest(semantics);
  }

  return {
    configPath,
    config,
    suite,
    ...(thresholdPolicy ? { thresholdPolicy } : {}),
    datasets,
    datasetPaths,
    datasetSnapshots,
    cases,
    ...(semantics ? { semantics } : {}),
    ...(pricing ? { pricing } : {}),
    digests: {
      config: contentDigest({ config, pricingDigest: pricingDigest ?? null }),
      suite: contentDigest(suite),
      datasets: contentDigest([...datasets.values()]),
      cases: contentDigest(cases.map((item) => item.contentId)),
      ...(semanticsDigest ? { semantics: semanticsDigest } : {}),
      ...(pricingDigest ? { pricing: pricingDigest } : {}),
      ...(thresholdPolicyDigest ? { thresholdPolicy: thresholdPolicyDigest } : {}),
    },
  };
}

export function resolveCredentialRef(
  ref: string | undefined,
  environment: NodeJS.ProcessEnv = process.env,
): string | undefined {
  if (!ref) return undefined;
  const name = ref.slice("env:".length);
  const value = environment[name]?.trim();
  if (!value) throw new Error(`required credential environment variable ${name} is not set`);
  return value;
}

export function publicVariant(variant: EvalVariant): EvalVariant {
  return structuredClone(variant);
}
