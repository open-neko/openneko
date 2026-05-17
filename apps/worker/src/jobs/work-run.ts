import type { AgentEvent } from "@neko/llm";
import {
  appendWorkRunEvent,
  getWorkRun,
  getWorkRunEvents,
  runChatTurn,
  scrubAgentEvent,
} from "@neko/llm/work";
import { getCurrentScrubber } from "../plugins/registry-instance.js";

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
  // Snapshot the scrubber once per run. fs.watch on the secrets file
  // rebuilds the registry's scrubber, so a future run picks up rotated
  // values; mid-run rotation is documented as out-of-scope.
  const scrubber = getCurrentScrubber();

  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({
      orgId,
      threadId,
      runId,
      seq,
      event: scrubAgentEvent(scrubber, event),
    });
  };

  await runChatTurn({ orgId, threadId, runId, message, emit });
}
