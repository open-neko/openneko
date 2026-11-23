/**
 * @neko/llm — shared LLM machinery: providers, agents, classifier.
 *
 * apps/web and apps/worker consume this package.
 */

export * from "./config";
export * from "./llm";
export { generate, hostProgram, type GenerateResult, type HostProgram } from "./inference";
export * from "./classifier";
export * from "./usage-normalization";
export * from "./agent-backend";
export { makeAgentBackend } from "./agent-runtime";
export {
  resolveAgentBackend,
  resolveAgentBackendId,
  resolveAgentConcurrency,
  type AgentConcurrency,
} from "./agent-backend-resolver";
export { cancelAllAgents, registerAgentCanceller } from "./agent-shutdown";
export {
  DataSourceAuthorizationError,
  UpstreamProviderError,
  detectAgentExecutionError,
  detectDataSourceAuthorizationError,
  detectUpstreamError,
} from "./agent-error";
export { AX_HARNESS_BINARY } from "./agent-runtime-contract";
export {
  ensureHostConfigProvisioned,
  loadAgentModelKeys,
  provisionHostConfig,
  verifyAgentRuntimeReady,
} from "./host-provision";
export {
  prefetchKnowledgePack,
  prefetchKnowledgeForOrg,
  prefetchAgenticKnowledgePack,
  knowledgePackPaths,
  discoveryUrlFromMcpUrl,
  type PrefetchKnowledgeResult,
  type KnowledgePackPaths,
} from "./knowledge-pack";
export * from "./work";
export * from "./library";
export * from "./workflows";
export * from "./graphjin";

export { runEmbeddingIndexJob, type EmbeddingIndexPayload } from "./embedding-jobs";
