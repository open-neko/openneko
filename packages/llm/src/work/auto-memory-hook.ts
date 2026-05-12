import { runWorkAutoMemoryPipeline } from "./auto-memory";

// { async: true } so the SDK doesn't block the user-facing turn on the
// classifier (5-15s Anthropic call).
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
