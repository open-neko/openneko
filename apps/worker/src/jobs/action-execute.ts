import type { ActionExecutePayload } from "@neko/db/jobs";
import { executeApprovedActionRequest } from "@neko/llm/workflows";

export async function runActionExecute(
  payload: ActionExecutePayload,
): Promise<void> {
  const result = await executeApprovedActionRequest(
    payload.orgId,
    payload.actionRequestId,
  );
  if (!result.ok) {
    console.warn(
      `[action-execute] action_request=${payload.actionRequestId} failed: ${result.error}`,
    );
  }
}
