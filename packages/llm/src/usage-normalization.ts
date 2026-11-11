import type { AgentTokenUsage } from "./agent-backend";

function addOptional(
  left: number | undefined,
  right: number | undefined,
): number | undefined {
  if (left === undefined && right === undefined) return undefined;
  return (left ?? 0) + (right ?? 0);
}

/** Add token/cost scopes without turning a missing scope into a zero. */
export function combineAgentTokenUsage(
  ...usages: readonly AgentTokenUsage[]
): AgentTokenUsage {
  const available = usages.some((usage) =>
    [usage.inputTokens, usage.outputTokens, usage.totalTokens].some(
      (value) => value !== undefined,
    ),
  );
  const missingReasons = usages.flatMap((usage) => usage.missingReasons ?? []);
  const combined = usages.reduce<AgentTokenUsage>(
    (total, usage) => ({
      inputTokens: addOptional(total.inputTokens, usage.inputTokens),
      outputTokens: addOptional(total.outputTokens, usage.outputTokens),
      cacheReadTokens: addOptional(
        total.cacheReadTokens,
        usage.cacheReadTokens,
      ),
      cacheWriteTokens: addOptional(
        total.cacheWriteTokens,
        usage.cacheWriteTokens,
      ),
      reasoningTokens: addOptional(
        total.reasoningTokens,
        usage.reasoningTokens,
      ),
      totalTokens: addOptional(total.totalTokens, usage.totalTokens),
      estimatedCostUsd: addOptional(
        total.estimatedCostUsd,
        usage.estimatedCostUsd,
      ),
      billedCostUsd: addOptional(total.billedCostUsd, usage.billedCostUsd),
      ...(usage.currency ?? total.currency
        ? { currency: usage.currency ?? total.currency }
        : {}),
      ...(usage.pricingCatalogVersion ?? total.pricingCatalogVersion
        ? {
            pricingCatalogVersion:
              usage.pricingCatalogVersion ?? total.pricingCatalogVersion,
          }
        : {}),
      ...(total.costStatus === "unknown" || usage.costStatus === "unknown"
        ? { costStatus: "unknown" as const }
        : usage.costStatus ?? total.costStatus
          ? { costStatus: usage.costStatus ?? total.costStatus }
          : {}),
      coverage: "unavailable",
    }),
    { coverage: "unavailable" },
  );
  return {
    ...combined,
    coverage: !available
      ? "unavailable"
      : usages.every((usage) => usage.coverage === "complete")
        ? "complete"
        : "partial",
    ...(missingReasons.length > 0
      ? { missingReasons: [...new Set(missingReasons)] }
      : {}),
  };
}
