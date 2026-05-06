import type { AgentBackend, AgentRunOptions } from "../agent-backend";
import { runHermes } from "../hermes-runner";

/**
 * Hermes backend — wraps the existing `runHermes` spawn helper. No
 * behavior change; this exists so callers depend on the AgentBackend
 * interface instead of a single implementation.
 */
export class HermesBackend implements AgentBackend {
  readonly id = "hermes" as const;

  run(opts: AgentRunOptions): Promise<string> {
    return runHermes(opts);
  }
}
