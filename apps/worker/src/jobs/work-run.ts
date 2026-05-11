import type { AgentEvent } from "@neko/llm";
import {
  appendWorkRunEvent,
  getWorkRun,
  getWorkRunEvents,
  runChatTurn,
} from "@neko/llm/work";

export async function runWorkRun(
  _jobId: string,
  orgId: string,
  payload: { runId: string; threadId: string; message: string },
): Promise<void> {
  const { runId, threadId, message } = payload;

  const run = await getWorkRun(orgId, runId);
  if (!run) {
    console.warn(
      `[work-run] run ${runId} not found for thread ${threadId}; skipping stale job`,
    );
    return;
  }

  let seq = (await getWorkRunEvents(orgId, runId)).length;

  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({ orgId, threadId, runId, seq, event });
  };

  await runChatTurn({ orgId, threadId, runId, message, emit });
}
