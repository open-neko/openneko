import {
  finishActionExecution,
  getActionRequest,
  markActionRequestExecuted,
  markActionRequestFailed,
  recordActionExecution,
  type ActionRequestRecord,
} from "./action-store";

export type ActionExecutionInput = {
  request: ActionRequestRecord;
};

export type ActionExecutionOutcome = {
  externalRef?: string | null;
  result?: Record<string, unknown> | null;
  commandOrOperation?: string | null;
};

export type ActionAdapter = (
  input: ActionExecutionInput,
) => Promise<ActionExecutionOutcome>;

const adapters = new Map<string, ActionAdapter>();
let defaultAdapter: ActionAdapter | null = null;

/** Register an executor for a specific action kind. Test-overridable. */
export function registerActionAdapter(
  kind: string,
  adapter: ActionAdapter,
): void {
  adapters.set(kind, adapter);
}

/** Set the fallback adapter used when no kind-specific adapter is registered. */
export function setDefaultActionAdapter(adapter: ActionAdapter | null): void {
  defaultAdapter = adapter;
}

export function getRegisteredActionKinds(): string[] {
  return Array.from(adapters.keys());
}

/**
 * Built-in mock adapter — records the action as executed without any
 * external side effect. Suitable for tests and demo runs while real
 * adapters (Slack, CRM, git, etc.) are being built. Returns the request's
 * payload as the execution result so callers can inspect what was
 * "executed" without leaving the local DB.
 */
export const mockActionAdapter: ActionAdapter = async ({ request }) => {
  return {
    commandOrOperation: `mock:${request.kind}`,
    externalRef: `mock-${request.id}`,
    result: {
      mocked: true,
      kind: request.kind,
      scope: request.scope,
      target: request.target,
      summary: request.summary,
      payload: request.payload,
    },
  };
};

setDefaultActionAdapter(mockActionAdapter);

export class ActionRequestNotApprovedError extends Error {
  constructor(public readonly status: string) {
    super(`action_request status=${status}; expected approved`);
    this.name = "ActionRequestNotApprovedError";
  }
}

/**
 * Execute an approved action_request. Writes an action_execution row,
 * runs the registered adapter (or the default mock adapter), updates
 * the execution + request status, and returns the final execution
 * record. Throws if the request isn't approved.
 */
export async function executeApprovedActionRequest(
  orgId: string,
  actionRequestId: string,
): Promise<{ ok: boolean; error?: string }> {
  const request = await getActionRequest(orgId, actionRequestId);
  if (!request) {
    throw new Error(`action_request ${actionRequestId} not found`);
  }
  if (request.status !== "approved") {
    throw new ActionRequestNotApprovedError(request.status);
  }

  const adapter = adapters.get(request.kind) ?? defaultAdapter;
  if (!adapter) {
    await markActionRequestFailed(
      request.id,
      `no adapter registered for kind "${request.kind}"`,
    );
    return {
      ok: false,
      error: `no adapter registered for kind "${request.kind}"`,
    };
  }

  const exec = await recordActionExecution({
    orgId,
    actionRequestId: request.id,
    executor: adapters.has(request.kind) ? request.kind : "default_mock",
    payload: request.payload,
  });

  try {
    const outcome = await adapter({ request });
    await finishActionExecution({
      id: exec.id,
      status: "succeeded",
      result: outcome.result ?? null,
      externalRef: outcome.externalRef ?? null,
    });
    await markActionRequestExecuted(request.id);
    return { ok: true };
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    await finishActionExecution({
      id: exec.id,
      status: "failed",
      error: msg,
    });
    await markActionRequestFailed(request.id, msg);
    return { ok: false, error: msg };
  }
}
