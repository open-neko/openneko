import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { z } from "zod";
import {
  AttemptSchema,
  CaseSchema,
  DatasetSchema,
  EpisodeSchema,
  EvalConfigSchema,
  ManifestSchema,
  GeneratorSchema,
  MetricQuestionInputSchema,
  OracleJournalSchema,
  PricingCatalogSchema,
  ResultLineSchema,
  ResultManifestSchema,
  ScoreSchema,
  SemanticRegistrySchema,
  SummarySchema,
  SuiteSchema,
  ThresholdPolicySchema,
  UsageSchema,
} from "../src/schemas";

const outputDirectory = fileURLToPath(
  new URL("../../../evals/schemas/", import.meta.url),
);
const schemas = {
  "config.v1.schema.json": EvalConfigSchema,
  "dataset.v1.schema.json": DatasetSchema,
  "suite.v1.schema.json": SuiteSchema,
  "threshold-policy.v1.schema.json": ThresholdPolicySchema,
  "case.v1.schema.json": CaseSchema,
  "attempt.v1.schema.json": AttemptSchema,
  "episode.v1.schema.json": EpisodeSchema,
  "score.v1.schema.json": ScoreSchema,
  "manifest.v1.schema.json": ManifestSchema,
  "semantics.v1.schema.json": SemanticRegistrySchema,
  "oracles.v1.schema.json": OracleJournalSchema,
  "usage.v1.schema.json": UsageSchema,
  "pricing.v1.schema.json": PricingCatalogSchema,
  "generator.v1.schema.json": GeneratorSchema,
  "summary.v1.schema.json": SummarySchema,
  "result.v1.schema.json": ResultLineSchema,
  "result-manifest.v1.schema.json": ResultManifestSchema,
} as const;

await mkdir(outputDirectory, { recursive: true });
for (const [name, schema] of Object.entries(schemas)) {
  const document = z.toJSONSchema(schema, { reused: "ref" }) as Record<
    string,
    unknown
  >;
  if (name === "case.v1.schema.json") {
    const { $schema: _nestedSchema, ...metricInput } = z.toJSONSchema(
      MetricQuestionInputSchema,
      { reused: "ref" },
    );
    document.allOf = [
      {
        if: {
          properties: { product_path: { const: "metric" } },
          required: ["product_path"],
        },
        then: { properties: { input: metricInput } },
      },
    ];
  }
  await writeFile(
    new URL(name, `file://${outputDirectory}/`),
    `${JSON.stringify(document, null, 2)}\n`,
    "utf8",
  );
}
