/**
 * @neko/llm — shared LLM machinery: providers, agents, classifier.
 *
 * apps/web and apps/worker consume this package.
 */

export * from "./agent-backend";
export {
  DataSourceAuthorizationError,
  UpstreamProviderError,
  detectAgentExecutionError,
  detectDataSourceAuthorizationError,
  detectUpstreamError,
} from "./agent-error";
export { AX_HARNESS_BINARY } from "./agent-runtime-contract";
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
