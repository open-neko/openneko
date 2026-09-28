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
  /** Stable trusted key; adapters use it only when their provider supports idempotency. */
  idempotencyKey?: string;
};

export type ActionExecutionOutcome = {
  externalRef?: string | null;
  result?: Record<string, unknown> | null;
  commandOrOperation?: string | null;
  changesetId?: string | null;
};

export type ActionAdapter = ((input: ActionExecutionInput) => Promise<ActionExecutionOutcome>) & {
  /** Read provider status only. Null means unknown, never permission to redispatch. */
  reconcile?: (input: ActionExecutionInput & {idempotencyKey:string}) => Promise<ActionExecutionOutcome | null>;
};

export type ActionAdapterResolver = (
  request: ActionRequestRecord,
) => Promise<ActionAdapter | null>;

const adapters = new Map<string, ActionAdapter>();
const adapterOrigins = new Map<string, "pack" | "plugin" | "internal">();
let fallbackAdapterResolver: ActionAdapterResolver | null = null;

/** Register an executor for a specific action kind. Test-overridable. */
export function registerActionAdapter(
  kind: string,
  adapter: ActionAdapter,
  origin: "pack" | "plugin" | "internal" = "internal",
): void {
  adapters.set(kind, adapter);
  adapterOrigins.set(kind, origin);
}

/** Register one resolver for action kinds supplied by installed packs. */
export function registerFallbackActionAdapterResolver(resolver: ActionAdapterResolver): () => void {
  fallbackAdapterResolver = resolver;
  return () => {
    if (fallbackAdapterResolver === resolver) fallbackAdapterResolver = null;
  };
}

export function getRegisteredActionKinds(): string[] {
  return Array.from(adapters.keys());
}

/** Native pack adapters only; plugin registrations cannot advertise a pack tool. */
export function getRegisteredPackActionKinds(): string[] {
  return Array.from(adapterOrigins).filter(([, origin]) => origin === "pack").map(([kind]) => kind);
}

async function resolveHarnessAdapter(
  request: ActionRequestRecord,
  source: "pack" | "plugin" | "internal",
): Promise<ActionAdapter | undefined> {
  if (source === "pack") {
    const declarative = await fallbackAdapterResolver?.(request);
    if (declarative) return declarative;
  }
  return adapterOrigins.get(request.kind) === source ? adapters.get(request.kind) : undefined;
}

export class ActionRequestNotApprovedError extends Error {
  constructor(public readonly status: string) {
    super(`action_request status=${status}; expected approved`);
    this.name = "ActionRequestNotApprovedError";
  }
}

/**
 * Adapter-side signal that execution may be retried without terminally
 * failing the action request. The current execution attempt is still logged
 * as failed; the approved request remains eligible for the queue retry.
 */
export class RetryableActionAdapterError extends Error {
  readonly code = "action_adapter_retryable";

  constructor(message: string, options: { cause?: unknown } = {}) {
    super(message, options);
    this.name = "RetryableActionAdapterError";
  }
}

/**
 * Execute an approved action_request. Writes an action_execution row,
 * runs the registered adapter, updates
 * the execution + request status, and returns the final execution
 * record. Throws if the request isn't approved.
 */
export async function executeApprovedActionRequest(
  orgId: string,
  actionRequestId: string,
): Promise<{
  ok: boolean;
  error?: string;
  outcome?: ActionExecutionOutcome;
}> {
  const request = await getActionRequest(orgId, actionRequestId);
  if (!request) {
    throw new Error(`action_request ${actionRequestId} not found`);
  }
  if (request.actorBackend === "harness") {
    if (!request.harnessOperationId || !request.harnessPrepared) throw new Error("Harness governed action execution is not enabled for an unprepared request");
    const {executeHarnessAction}=await import("./harness-executor");
    return executeHarnessAction(request,source=>resolveHarnessAdapter(request,source));
  }
  if (request.status !== "approved") throw new ActionRequestNotApprovedError(request.status);

  const adapter = adapters.get(request.kind) ?? await fallbackAdapterResolver?.(request) ?? undefined;
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
    executor: request.kind,
    payload: request.payload,
  });

  try {
    const outcome = await adapter({ request });
    await finishActionExecution({
      id: exec.id,
      status: "succeeded",
      result: outcome.result ?? null,
      externalRef: outcome.externalRef ?? null,
      changesetId: outcome.changesetId ?? null,
      commandOrOperation: outcome.commandOrOperation ?? null,
    });
    await markActionRequestExecuted(request.id);
    return { ok: true, outcome };
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    await finishActionExecution({
      id: exec.id,
      status: "failed",
      error: msg,
    });
    if (err instanceof RetryableActionAdapterError) throw err;
    await markActionRequestFailed(request.id, msg);
    return { ok: false, error: msg };
  }
}
