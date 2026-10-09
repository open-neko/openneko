import type {
  AgentBackend,
  AgentBackendId,
  AgentModelIdentity,
} from "./agent-backend";
import { AxBackend, type AxBackendConfig } from "./agent-backends/ax";
import { HermesBackend } from "./agent-backends/hermes";

export type { AxBackendConfig };

/** Pure runtime factory used inside the OpenShell agent image. */
export function makeAgentBackend(cfg: {
  id: AgentBackendId;
  configuredIdentity?: AgentModelIdentity;
  ax?: AxBackendConfig;
}): AgentBackend {
  if (cfg.id === "hermes") return new HermesBackend(cfg.configuredIdentity);
  if (cfg.id === "ax") return new AxBackend(cfg.ax);
  throw new Error(`Unsupported agent backend: ${String(cfg.id)}`);
}
