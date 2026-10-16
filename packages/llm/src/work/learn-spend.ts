import type { AxProgramUsage } from "@ax-llm/ax";
import { recordUsageSpend } from "../spend/ledger";
import { estimateTokenCostUsd, MODEL_PRICES_VERSION } from "../spend/model-prices";
import { normalizeAxProgramUsage } from "../usage-normalization";

/** The usage surface every Ax generator has, for any provider. */
export type UsageTracked = {
  getUsage(): AxProgramUsage[];
  resetUsage(): void;
};

/**
 * Records a learner model call in the spend ledger as system spend with no
 * run, then clears the generator's usage so the next call counts only once.
 * A ledger failure never fails the learner.
 */
export async function recordLearnerSpend(
  orgId: string,
  generator: UsageTracked,
  label: string,
  record: typeof recordUsageSpend = recordUsageSpend,
): Promise<void> {
  const calls = generator.getUsage();
  generator.resetUsage();
  if (calls.length === 0) return;
  const usage = normalizeAxProgramUsage(calls);
  const last = calls.at(-1);
  const cost = last?.ai && last.model ? estimateTokenCostUsd(last.ai, last.model, usage) : undefined;
  await record({
    orgId,
    source: "system",
    usage:
      cost === undefined
        ? { ...usage, costStatus: "unknown" }
        : {
            ...usage,
            costStatus: "estimated",
            estimatedCostUsd: cost,
            currency: "USD",
            costSource: "official_docs_snapshot",
            pricingCatalogVersion: MODEL_PRICES_VERSION,
          },
    ...(last?.ai ? { provider: last.ai } : {}),
    ...(last?.model ? { model: last.model } : {}),
  }).catch((error: unknown) =>
    console.warn(`[${label}] spend record failed: ${error instanceof Error ? error.message : error}`),
  );
}
