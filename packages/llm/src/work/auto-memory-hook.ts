import { enqueue, QUEUE } from "@neko/db/jobs";

// { async: true } so the SDK doesn't block the user-facing turn. Enqueues a
// pg-boss job so a worker crash mid-classifier doesn't silently drop the memory.
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
      enqueue(QUEUE.WORK_AUTO_MEMORY, {
        orgId: ctx.orgId,
        threadId: ctx.threadId,
        runId: ctx.runId,
        userMessage: ctx.userMessage,
        agentAnswer: lastMessage,
      }).catch((err) => {
        console.error("[work-auto-memory] enqueue failed:", err);
      });
    }
    return { async: true };
  };
}
