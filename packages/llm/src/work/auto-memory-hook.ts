import { runWorkAutoMemoryPipeline } from "./auto-memory";

/**
 * Stop-hook factory for the Claude Agent SDK.
 *
 * Returns a hook function that the SDK invokes when the assistant turn is
 * stopping (just before the `result` message). The hook returns
 * `{ async: true }` so the SDK doesn't block on the classifier — Anthropic
 * Messages API for the memory classifier can take 5-15s and we don't want
 * to extend the user-facing turn.
 *
 * Errors in the pipeline are logged and swallowed; the user already got
 * their answer. (Crash-safety against a worker restart mid-pipeline is a
 * separate problem — see Phase 9 plan: enqueue a persistent pg-boss
 * WORK_AUTO_MEMORY job here instead of `setImmediate`.)
 *
 * For Hermes runs the SDK hook doesn't exist; runChatTurn falls back to
 * a post-completion call (still fire-and-forget).
 */
export function makeAutoMemoryStopHook(ctx: {
  orgId: string;
  threadId: string;
  runId: string;
  userMessage: string;
}) {
  return async (
    input: Record<string, unknown>,
  ): Promise<Record<string, unknown>> => {
    const lastMessage =
      typeof input.last_assistant_message === "string"
        ? input.last_assistant_message
        : "";
    if (lastMessage.trim()) {
      setImmediate(() => {
        runWorkAutoMemoryPipeline({
          orgId: ctx.orgId,
          threadId: ctx.threadId,
          runId: ctx.runId,
          userMessage: ctx.userMessage,
          agentAnswer: lastMessage,
        }).catch((err) => {
          console.error("[work-auto-memory] stop-hook pipeline failed:", err);
        });
      });
    }
    return { async: true };
  };
}
