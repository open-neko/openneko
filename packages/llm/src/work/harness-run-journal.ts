import { createHash } from "node:crypto";
import { startupEvent } from "@neko/telemetry/startup";
import { pool } from "@neko/db";
import type { AgentRunResult } from "../agent-backend";
import { admitHarnessLaunch, checkHarnessAdmission, type HarnessReconciliation } from "./harness-launch-journal";

/** A dedicated PG session owns a run across hosts. Continuation requires scope validation and recovered evidence. */
export async function withHarnessRunJournal<T>(
  scope: {orgId: string; runId: string},
  run: (signal: AbortSignal, journal: HarnessRunJournal) => Promise<T>,
): Promise<T> {
  const client = await pool().connect();
  const abort = new AbortController();
  const key = JSON.stringify(["harness", scope.orgId, scope.runId]);
  const lost = () => { abort.abort(); startupEvent("sandbox.harness_database_owner", {outcome:"lost"}); };
  client.on("error", lost);
  client.on("end", lost);
  let locked = false;
  try {
    const lock = await client.query("SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS locked", [key]);
    locked = lock.rows[0]?.locked === true;
    startupEvent("sandbox.harness_database_owner", {outcome: locked ? "acquired" : "busy"});
    if (!locked) throw new Error("Harness launcher still active on another host");
    const journal: HarnessRunJournal = async (root, identity, reconcile, restorePrompt) => {
      const hash = (value: unknown) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
      const candidate = identity as Record<string, unknown> | null;
      const operationLimit = candidate?.maxOperations ?? 4;
      if (!Number.isInteger(operationLimit) || Number(operationLimit) < 1 || Number(operationLimit) > 32) {
        throw new Error("Invalid trusted Harness operation limit");
      }
      // Only a separately bound user request lets regenerated context differ.
      const context = restorePrompt && candidate && typeof candidate.prompt === "string" &&
        typeof candidate.userMessage === "string" && candidate.userMessage.trim()
        ? {prompt: candidate.prompt, scopeFingerprint: hash({...candidate, prompt: null})} : null;
      const existing = (await client.query(
        "SELECT fingerprint, result, accepted_context, operation_limit FROM harness_run_journal WHERE org_id=$1 AND run_id=$2",
        [scope.orgId, scope.runId],
      )).rows[0];
      if (existing?.accepted_context != null) {
        const saved = existing.accepted_context;
        if (!context || typeof saved.prompt !== "string" || saved.scopeFingerprint !== context.scopeFingerprint) {
          throw new Error("Harness launch conflicts with accepted input or authorization scope");
        }
        identity = {...candidate, prompt: saved.prompt};
      }
      const fingerprint = hash(identity);
      if (existing && (existing.fingerprint !== fingerprint || existing.operation_limit !== operationLimit)) {
        throw new Error("Harness launch conflicts with accepted input or authorization scope");
      }
      await checkHarnessAdmission(root, fingerprint);
      if (existing?.accepted_context != null) {
        restorePrompt!(existing.accepted_context.prompt);
        startupEvent("sandbox.harness_context", {outcome:"restored"});
      }
      const inserted = await client.query(
        "INSERT INTO harness_run_journal (org_id, run_id, fingerprint, accepted_context, operation_limit) VALUES ($1,$2,$3,$4::jsonb,$5) ON CONFLICT DO NOTHING RETURNING run_id",
        [scope.orgId, scope.runId, fingerprint, context ? JSON.stringify(context) : null, operationLimit],
      );
      const save = async (result: AgentRunResult) => {
        const encoded = JSON.stringify(result);
        if (Buffer.byteLength(encoded) > 8*1024*1024) throw new Error("Harness result receipt exceeds limit");
        if (abort.signal.aborted) throw new Error("Harness database ownership lost");
        await client.query("UPDATE harness_run_journal SET result=$3::jsonb, updated_at=now() WHERE org_id=$1 AND run_id=$2",
          [scope.orgId, scope.runId, encoded]);
      };
      if (inserted.rowCount) {
        // Preserve pre-database admissions on this host. Never discard old uncertainty.
        const local = await admitHarnessLaunch(root, identity, reconcile);
        if (local.result) { await save(local.result); return local; }
        return {result: undefined, resume: local.resume, complete: async (result: AgentRunResult) => {
          await save(result);
          await local.complete!(result);
        }};
      }
      const row = (await client.query("SELECT fingerprint, result, operation_limit FROM harness_run_journal WHERE org_id=$1 AND run_id=$2",
        [scope.orgId, scope.runId])).rows[0];
      if (!row || row.fingerprint !== fingerprint || row.operation_limit !== operationLimit) throw new Error("Harness launch conflicts with accepted input or authorization scope");
      if (row.result !== null) {
        if (!["completed", "failed", "cancelled"].includes(row.result?.status) || typeof row.result?.finalText !== "string") {
          throw new Error("Harness launch outcome unknown: invalid database receipt");
        }
        startupEvent("sandbox.harness_database_receipt", {outcome:"restored"});
        return {result: row.result as AgentRunResult, complete: undefined};
      }
      const result = await reconcile();
      if (abort.signal.aborted) throw new Error("Harness database ownership lost");
      if (result === "resume") {
        startupEvent("sandbox.harness_continuation", {outcome:"admitted"});
        return {result: undefined, complete: save, resume: true};
      }
      await save(result);
      startupEvent("sandbox.harness_database_receipt", {outcome:"reconciled"});
      return {result, complete: undefined};
    };
    const result = await run(abort.signal, journal);
    if (abort.signal.aborted) throw new Error("Harness database ownership lost");
    return result;
  } finally {
    // Destroy the connection on uncertain unlock; a pooled advisory lock is unsafe.
    let destroy = abort.signal.aborted;
    if (locked && !destroy) {
      try { await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [key]); }
      catch { destroy = true; }
    }
    client.removeListener("error", lost);
    client.removeListener("end", lost);
    client.release(destroy);
  }
}

export type HarnessRunJournal = (
  root: string, identity: unknown, reconcile: () => Promise<HarnessReconciliation>, restorePrompt?: (prompt: string) => void,
) => ReturnType<typeof admitHarnessLaunch>;
