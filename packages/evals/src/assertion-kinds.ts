import type { EvalCase } from "./schemas";

export type AssertionDimension = EvalCase["assertions"][number]["dimension"];

type KindTable = Readonly<Record<string, readonly AssertionDimension[]>>;

// The skill-learning adapter wraps the work-backend scorer.
const WORK_BACKEND_KINDS: KindTable = {
  "answer.numeric-relative-error": ["ground_truth"],
  "answer.window-exact": ["ground_truth"],
  "answer.fixture-sentinel": ["ground_truth"],
  "answer.all-fixture-sentinels": ["ground_truth"],
  "answer.exact": ["ground_truth"],
  "answer.contains": ["ground_truth"],
  "answer.excludes-decoy": ["safety"],
  "answer.excludes-all-decoys": ["safety"],
  "answer.excludes-fixture-sentinel": ["safety"],
  "answer.oracle-dimension": ["ground_truth"],
  "answer.labelled-contract": ["behavior"],
  "answer.clean": ["behavior"],
  "state.sequence-exact": ["method"],
  "state.terminal-subset": ["ground_truth"],
  "state.collateral-subset": ["safety"],
  "state.no-approval-bypass": ["safety"],
  "trace.graphjin-execute": ["method"],
  "trace.graphjin-no-delegation": ["method", "safety"],
  "trace.blocked-mutation": ["behavior"],
  "trace.no-successful-mutation": ["safety"],
  "trace.graphjin-api-mutation": ["method"],
  "trace.memory-search": ["method"],
  "trace.memory-prefetched": ["method"],
  "trace.skill-loaded": ["method"],
  "trace.skill-not-loaded": ["method", "safety"],
  "trace.library-search": ["method"],
  "trace.workflow-list": ["method"],
  "trace.no-cross-tenant-resource": ["safety"],
  "trace.no-unauthorized-channel-action": ["safety"],
  "trace.no-skill-loaded": ["method", "safety"],
  "answer.numeric-mismatch": ["ground_truth"],
};

// Scenario cases (`input.scenario: steps`) run ordered steps against one org.
// These kinds read the scenario observation, not one turn.
const SCENARIO_KINDS: KindTable = {
  "state.memory-saved": ["method", "ground_truth"],
  "state.memory-unchanged": ["behavior", "safety"],
  "trace.memory-used": ["method"],
  "state.library-cataloged": ["method"],
  "trace.library-document-retrieved": ["method"],
  "state.skill-learn-event": ["method"],
  "state.learned-rules": ["method", "safety"],
  "trace.skill-overlay": ["method"],
  "state.skill-discovery": ["method"],
  "trace.learned-skill-loaded": ["method"],
  "state.learned-skill-cap": ["safety"],
  "state.catalog-distinct": ["behavior"],
  "state.skill-archived": ["method"],
};

// The lifecycle and work-memory scorers read `params.path` for every
// assertion, so `boolean.path` is their only kind.
export const ASSERTION_KINDS: Readonly<Record<string, KindTable>> = {
  "adventureworks.metric": {
    "numeric.relative-error": ["ground_truth"],
    "numeric.baseline-relative-error": ["ground_truth"],
    "time-window.exact": ["ground_truth"],
    "metric.contract": ["behavior"],
    "dimension.contains": ["ground_truth"],
    "duration.under": ["efficiency"],
  },
  "openneko.work-backend": { ...WORK_BACKEND_KINDS, ...SCENARIO_KINDS },
  "openneko.lifecycle": {
    "boolean.path": ["ground_truth", "behavior", "safety"],
  },
  "openneko.work-memory": {
    "boolean.path": ["ground_truth", "method", "behavior", "safety"],
  },
  "openneko.skill-learning": {
    ...WORK_BACKEND_KINDS,
    "learning.rule-grounded": ["safety"],
    "learning.no-leak": ["safety"],
    "learning.overlay-applied": ["method"],
    "learning.overlay-absent": ["method"],
  },
};

export function isRegisteredAdapter(adapter: string): boolean {
  return Object.hasOwn(ASSERTION_KINDS, adapter);
}

export function assertionKindErrors(
  adapter: string,
  evalCase: Pick<EvalCase, "id" | "assertions">,
  registry: Readonly<Record<string, KindTable>> = ASSERTION_KINDS,
): string[] {
  const kinds = registry[adapter];
  if (!kinds) return [`adapter ${adapter} has no assertion kind registry`];
  const errors: string[] = [];
  for (const assertion of evalCase.assertions) {
    const label = `case ${evalCase.id} assertion ${assertion.id}`;
    const dimensions = Object.hasOwn(kinds, assertion.kind)
      ? kinds[assertion.kind]!
      : undefined;
    if (!dimensions) {
      errors.push(
        `${label}: kind ${assertion.kind} is not registered for adapter ${adapter}`,
      );
    } else if (!dimensions.includes(assertion.dimension)) {
      errors.push(
        `${label}: kind ${assertion.kind} allows dimension ${dimensions.join(" or ")}, not ${assertion.dimension}`,
      );
    }
  }
  return errors;
}
