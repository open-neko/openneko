import type {
  AgentBackend,
  AgentBackendId,
  AgentModelIdentity,
} from "./agent-backend";
import { HarnessBackend } from "./agent-backends/harness";
import { HermesBackend } from "./agent-backends/hermes";

/** Pure runtime factory used inside the OpenShell agent image. */
export function makeAgentBackend(cfg: {
  id: AgentBackendId;
  configuredIdentity?: AgentModelIdentity;
}): AgentBackend {
  if (cfg.id === "harness") return new HarnessBackend(cfg.configuredIdentity);
  if (cfg.id !== "hermes") {
    throw new Error(`Unsupported agent backend: ${String(cfg.id)}`);
  }
  return new HermesBackend(cfg.configuredIdentity);
}
