import type { Pool, PoolClient } from "pg";

const DEFAULT_LEASE_MS = 60_000;

export type RecordsActionExecutionStatus =
  | "claimed"
  | "running"
  | "succeeded"
  | "failed";

export type RecordsActionExecution = {
  actionRequestId: string;
  orgId: string;
  appId: string | null;
  actionKind: string;
  status: RecordsActionExecutionStatus;
  leaseOwner: string | null;
  leaseExpiresAt: Date | null;
  result: Record<string, unknown> | null;
  error: Record<string, unknown> | null;
  startedAt: Date;
  finishedAt: Date | null;
};

type RawExecution = {
  action_request_id: string;
  org_id: string;
  app_id: string | null;
  action_kind: string;
  status: RecordsActionExecutionStatus;
  lease_owner: string | null;
  lease_expires_at: Date | null;
  result: Record<string, unknown> | null;
  error: Record<string, unknown> | null;
  started_at: Date;
  finished_at: Date | null;
};

type RawExecutionWithExpiry = RawExecution & { lease_expired: boolean };

export type RecordsActionClaim =
  | { kind: "claimed"; execution: RecordsActionExecution }
  | { kind: "replay"; execution: RecordsActionExecution; result: Record<string, unknown> };

export class RecordsActionLeaseBusyError extends Error {
  readonly code = "records_action_lease_busy";

  constructor(
    readonly actionRequestId: string,
    readonly leaseExpiresAt: Date | null,
  ) {
    super(`records action ${actionRequestId} is already leased`);
    this.name = "RecordsActionLeaseBusyError";
  }
}

export class RecordsActionTerminalError extends Error {
  readonly code = "records_action_terminal_failure";

  constructor(
    readonly actionRequestId: string,
    readonly executionError: Record<string, unknown> | null,
  ) {
    super(`records action ${actionRequestId} already failed`);
    this.name = "RecordsActionTerminalError";
  }
}

export class RecordsActionLeaseLostError extends Error {
  readonly code = "records_action_lease_lost";

  constructor(readonly actionRequestId: string) {
    super(`records action ${actionRequestId} lease is no longer owned by this worker`);
    this.name = "RecordsActionLeaseLostError";
  }
}

function validateLeaseMs(leaseMs: number): number {
  if (!Number.isSafeInteger(leaseMs) || leaseMs < 1_000 || leaseMs > 15 * 60_000) {
    throw new Error("records action lease must be between 1 second and 15 minutes");
  }
  return leaseMs;
}

function mapExecution(row: RawExecution): RecordsActionExecution {
  return {
    actionRequestId: row.action_request_id,
    orgId: row.org_id,
    appId: row.app_id,
    actionKind: row.action_kind,
    status: row.status,
    leaseOwner: row.lease_owner,
    leaseExpiresAt: row.lease_expires_at,
    result: row.result,
    error: row.error,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
  };
}

const EXECUTION_COLUMNS = `action_request_id, org_id, app_id, action_kind,
  status, lease_owner, lease_expires_at, result, error, started_at, finished_at`;

async function transaction<T>(client: PoolClient, operation: () => Promise<T>): Promise<T> {
  await client.query("begin");
  try {
    const result = await operation();
    await client.query("commit");
    return result;
  } catch (error) {
    await client.query("rollback");
    throw error;
  }
}

function assertIdentity(
  execution: RecordsActionExecution,
  expected: { orgId: string; appId: string | null; actionKind: string },
): void {
  if (
    execution.orgId !== expected.orgId ||
    execution.appId !== expected.appId ||
    execution.actionKind !== expected.actionKind
  ) {
    throw new Error(
      `records action ${execution.actionRequestId} was reused with a different identity`,
    );
  }
}

export async function getRecordsActionExecution(
  pool: Pool,
  actionRequestId: string,
): Promise<RecordsActionExecution | null> {
  const result = await pool.query<RawExecution>(
    `select ${EXECUTION_COLUMNS}
     from engine.action_execution where action_request_id = $1`,
    [actionRequestId],
  );
  return result.rows[0] ? mapExecution(result.rows[0]) : null;
}

/** Atomically create, replay, or reclaim the command-level receipt. */
export async function claimRecordsActionExecution(
  pool: Pool,
  input: {
    actionRequestId: string;
    orgId: string;
    appId: string | null;
    actionKind: string;
    leaseOwner: string;
    leaseMs?: number;
  },
): Promise<RecordsActionClaim> {
  if (
    !input.actionRequestId.trim() ||
    !input.orgId.trim() ||
    !input.actionKind.trim() ||
    !input.leaseOwner.trim()
  ) {
    throw new Error("records action claim requires request, org, kind, and lease owner");
  }
  const leaseMs = validateLeaseMs(input.leaseMs ?? DEFAULT_LEASE_MS);
  const client = await pool.connect();
  try {
    return await transaction(client, async () => {
      const inserted = await client.query<RawExecution>(
        `insert into engine.action_execution
           (action_request_id, org_id, app_id, action_kind, status,
            lease_owner, lease_expires_at)
         values ($1, $2, $3, $4, 'running', $5,
                 now() + ($6::bigint * interval '1 millisecond'))
         on conflict (action_request_id) do nothing
         returning ${EXECUTION_COLUMNS}`,
        [
          input.actionRequestId,
          input.orgId,
          input.appId,
          input.actionKind,
          input.leaseOwner,
          leaseMs,
        ],
      );
      if (inserted.rows[0]) {
        return { kind: "claimed", execution: mapExecution(inserted.rows[0]) };
      }

      const currentResult = await client.query<RawExecutionWithExpiry>(
        `select ${EXECUTION_COLUMNS}, lease_expires_at <= now() as lease_expired
         from engine.action_execution
         where action_request_id = $1 for update`,
        [input.actionRequestId],
      );
      const currentRow = currentResult.rows[0];
      if (!currentRow) throw new Error("records action receipt disappeared during claim");
      const current = mapExecution(currentRow);
      assertIdentity(current, input);
      if (current.status === "succeeded") {
        if (!current.result) {
          throw new Error(`records action ${input.actionRequestId} succeeded without a result`);
        }
        return { kind: "replay", execution: current, result: current.result };
      }
      if (current.status === "failed") {
        throw new RecordsActionTerminalError(input.actionRequestId, current.error);
      }
      // Durable queue retries keep the same lease-owner identity. Let that
      // logical job resume immediately after a process/database restart; a
      // different job or worker still waits for expiry and cannot steal it.
      if (current.leaseOwner === input.leaseOwner) {
        const resumed = await client.query<RawExecution>(
          `update engine.action_execution
           set status = 'running',
               lease_expires_at = now() + ($3::bigint * interval '1 millisecond'),
               error = null, finished_at = null
           where action_request_id = $1 and lease_owner = $2
             and status in ('claimed', 'running')
           returning ${EXECUTION_COLUMNS}`,
          [input.actionRequestId, input.leaseOwner, leaseMs],
        );
        if (!resumed.rows[0]) {
          throw new Error(
            `records action ${input.actionRequestId} disappeared during same-owner resume`,
          );
        }
        return { kind: "claimed", execution: mapExecution(resumed.rows[0]) };
      }
      const expired = current.leaseExpiresAt === null || currentRow.lease_expired;
      if (!expired) {
        throw new RecordsActionLeaseBusyError(
          input.actionRequestId,
          current.leaseExpiresAt,
        );
      }

      const reclaimed = await client.query<RawExecution>(
        `update engine.action_execution
         set status = 'running', lease_owner = $2,
             lease_expires_at = now() + ($3::bigint * interval '1 millisecond'),
             error = null, finished_at = null
         where action_request_id = $1
         returning ${EXECUTION_COLUMNS}`,
        [input.actionRequestId, input.leaseOwner, leaseMs],
      );
      return { kind: "claimed", execution: mapExecution(reclaimed.rows[0]!) };
    });
  } finally {
    client.release();
  }
}

export async function renewRecordsActionExecutionLease(
  pool: Pool,
  input: { actionRequestId: string; leaseOwner: string; leaseMs?: number },
): Promise<RecordsActionExecution> {
  const leaseMs = validateLeaseMs(input.leaseMs ?? DEFAULT_LEASE_MS);
  const result = await pool.query<RawExecution>(
    `update engine.action_execution
     set lease_expires_at = now() + ($3::bigint * interval '1 millisecond')
     where action_request_id = $1 and lease_owner = $2 and status = 'running'
       and lease_expires_at > now()
     returning ${EXECUTION_COLUMNS}`,
    [input.actionRequestId, input.leaseOwner, leaseMs],
  );
  if (!result.rows[0]) throw new RecordsActionLeaseLostError(input.actionRequestId);
  return mapExecution(result.rows[0]);
}

/**
 * Publish worker-owned execution context for same-transaction substrate
 * hooks, while renewing the command lease. Import batches use this to bind
 * the app-row mutation to its target-side batch receipt.
 */
export async function setRecordsActionExecutionContext(
  pool: Pool,
  input: {
    actionRequestId: string;
    leaseOwner: string;
    context: Record<string, unknown>;
    leaseMs?: number;
  },
): Promise<RecordsActionExecution> {
  const leaseMs = validateLeaseMs(input.leaseMs ?? DEFAULT_LEASE_MS);
  const result = await pool.query<RawExecution>(
    `update engine.action_execution
     set result = $3::jsonb,
         lease_expires_at = now() + ($4::bigint * interval '1 millisecond')
     where action_request_id = $1 and lease_owner = $2 and status = 'running'
       and lease_expires_at > now()
     returning ${EXECUTION_COLUMNS}`,
    [
      input.actionRequestId,
      input.leaseOwner,
      JSON.stringify(input.context),
      leaseMs,
    ],
  );
  if (!result.rows[0]) {
    throw new RecordsActionLeaseLostError(input.actionRequestId);
  }
  return mapExecution(result.rows[0]);
}

export async function succeedRecordsActionExecution(
  pool: Pool,
  input: {
    actionRequestId: string;
    leaseOwner: string;
    result: Record<string, unknown>;
  },
): Promise<RecordsActionExecution> {
  const updated = await pool.query<RawExecution>(
    `update engine.action_execution
     set status = 'succeeded', result = $3::jsonb, error = null,
         lease_owner = null, lease_expires_at = null, finished_at = now()
     where action_request_id = $1 and lease_owner = $2 and status = 'running'
     returning ${EXECUTION_COLUMNS}`,
    [input.actionRequestId, input.leaseOwner, JSON.stringify(input.result)],
  );
  if (updated.rows[0]) return mapExecution(updated.rows[0]);

  const existing = await getRecordsActionExecution(pool, input.actionRequestId);
  if (existing?.status === "succeeded" && existing.result) return existing;
  throw new RecordsActionLeaseLostError(input.actionRequestId);
}

/** Repair a lost engine receipt only when the same-transaction mutation audit
 * proves this exact action committed. The caller must also validate its frozen
 * request and derive the expected mutation ID; no GraphJin write occurs here. */
export async function recoverRecordsActionExecutionFromAudit(
  pool: Pool,
  input: {
    actionRequestId: string;
    orgId: string;
    appId: string;
    objectApiName: string;
    actionKind: string;
    action: "create" | "update" | "delete" | "restore";
    recordId: string;
    mutationId: string;
    result: Record<string, unknown>;
  },
): Promise<RecordsActionExecution | null> {
  const updated = await pool.query<RawExecution>(
    `update engine.action_execution execution
     set status='succeeded', result=$8::jsonb, error=null,
         lease_owner=null, lease_expires_at=null, finished_at=now()
     where execution.action_request_id=$1 and execution.org_id=$2
       and execution.app_id=$3 and execution.action_kind=$4
       and execution.status in ('claimed','running','failed')
       and exists (
         select 1 from engine.record_change_log audit
         where audit.action_request_id=$1 and audit.org_id=$2
           and audit.app_id=$3 and audit.object_api_name=$5
           and audit.record_id=$6 and audit.mutation_id=$7
           and audit.action=$9
       )
     returning ${EXECUTION_COLUMNS}`,
    [input.actionRequestId, input.orgId, input.appId, input.actionKind,
      input.objectApiName, input.recordId, input.mutationId,
      JSON.stringify(input.result), input.action],
  );
  return updated.rows[0] ? mapExecution(updated.rows[0]) : null;
}

export async function failRecordsActionExecution(
  pool: Pool,
  input: {
    actionRequestId: string;
    leaseOwner: string;
    error: Record<string, unknown>;
  },
): Promise<RecordsActionExecution> {
  const updated = await pool.query<RawExecution>(
    `update engine.action_execution
     set status = 'failed', error = $3::jsonb,
         lease_owner = null, lease_expires_at = null, finished_at = now()
     where action_request_id = $1 and lease_owner = $2 and status = 'running'
     returning ${EXECUTION_COLUMNS}`,
    [input.actionRequestId, input.leaseOwner, JSON.stringify(input.error)],
  );
  if (!updated.rows[0]) throw new RecordsActionLeaseLostError(input.actionRequestId);
  return mapExecution(updated.rows[0]);
}
