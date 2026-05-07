/**
 * @neko/llm — shared LLM machinery: providers, agents, classifier.
 *
 * Both apps/web (sync RPC: classify, provider-test) and apps/worker (job
 * handlers: profiler, industry-researcher, bootstrap-metrics-writer,
 * metric-agent) consume this package. Keeping it shared lets the web app
 * call LLM functions in-process instead of round-tripping through the
 * worker over HTTP.
 */

export * from "./config";
export * from "./llm";
export * from "./classifier";
export * from "./metric-agent";
export * from "./profiler";
export * from "./bootstrap-metrics-writer";
export * from "./industry-researcher";
export * from "./agent-backend";
export {
  resolveAgentBackend,
  resolveAgentBackendId,
  resolveAgentConcurrency,
  type AgentConcurrency,
} from "./agent-backend-resolver";
export { cancelAllAgents, registerAgentCanceller } from "./agent-shutdown";
export {
  UpstreamProviderError,
  detectUpstreamError,
} from "./agent-error";
export { provisionHostConfig } from "./host-provision";
export * from "./work";
