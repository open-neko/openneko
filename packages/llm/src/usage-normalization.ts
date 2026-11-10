import type { AxProgramUsage } from "@ax-llm/ax";
import type { AgentTokenUsage } from "./agent-backend";

function addOptional(
  left: number | undefined,
  right: number | undefined,
): number | undefined {
  if (left === undefined && right === undefined) return undefined;
  return (left ?? 0) + (right ?? 0);
}

/** Normalize every provider call made by an Ax program, including retries. */
export function normalizeAxProgramUsage(
  usages: readonly AxProgramUsage[],
): AgentTokenUsage {
  const tokenUsages = usages.flatMap((usage) =>
    usage.tokens ? [usage.tokens] : [],
  );
  if (tokenUsages.length === 0) {
    return {
      coverage: "unavailable",
      missingReasons: ["Ax program returned no token usage"],
    };
  }
  const complete = tokenUsages.length === usages.length;
  const sum = (select: (usage: (typeof tokenUsages)[number]) => number) =>
    tokenUsages.reduce((total, usage) => total + select(usage), 0);
  const optionalSum = (
    select: (usage: (typeof tokenUsages)[number]) => number | undefined,
  ): number | undefined => {
    const values = tokenUsages
      .map(select)
      .filter((value): value is number => value !== undefined);
    return values.length > 0
      ? values.reduce((total, value) => total + value, 0)
      : undefined;
  };
  return {
    inputTokens: sum((usage) => usage.promptTokens),
    outputTokens: sum((usage) => usage.completionTokens),
    cacheReadTokens: optionalSum((usage) => usage.cacheReadTokens),
    cacheWriteTokens: optionalSum((usage) => usage.cacheCreationTokens),
    reasoningTokens: optionalSum(
      (usage) => usage.reasoningTokens ?? usage.thoughtsTokens,
    ),
    totalTokens: sum((usage) => usage.totalTokens),
    coverage: complete ? "complete" : "partial",
    ...(!complete
      ? { missingReasons: ["Some Ax program calls omitted token usage"] }
      : {}),
  };
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
