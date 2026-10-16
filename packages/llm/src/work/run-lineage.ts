import {
  and,
  db,
  eq,
  run_channel_message,
  run_store_version,
  skill_learn_event,
  sql,
  work_run,
} from "@neko/db";
import { learnTargetDir, type LearnTarget } from "./learn-target";
import { readRulesAt, type LearnedRule } from "./skill-learn-playbook";
import { getOrgAgentRoot } from "./workspace";

export type RunLineage = { parentRunId: string | null; rootRunId: string | null };

/** A sub-run's root is its parent's root, or the parent itself. */
export async function resolveRunLineage(orgId: string, parentRunId?: string | null): Promise<RunLineage> {
  if (!parentRunId) return { parentRunId: null, rootRunId: null };
  const [parent] = await db()
    .select({ id: work_run.id, rootRunId: work_run.root_run_id })
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, parentRunId)))
    .limit(1);
  if (!parent) throw new Error(`parent run ${parentRunId} not found`);
  return { parentRunId: parent.id, rootRunId: parent.rootRunId ?? parent.id };
}

/** The run whose settlement and store versions a run shares. */
export async function rootRunOf(orgId: string, runId: string): Promise<string | null> {
  const [run] = await db()
    .select({ rootRunId: work_run.root_run_id })
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (!run) return null;
  return run.rootRunId ?? runId;
}

/** The live version of each learned store: the id of the event that last applied or rolled it back. */
export async function currentStoreVersions(
  orgId: string,
): Promise<Array<{ targetKind: string; targetId: string; version: string }>> {
  const rows = await db().execute<{ target_kind: string; target_id: string; id: string }>(sql`
    select distinct on (target_kind, target_id) target_kind, target_id, id
    from ${skill_learn_event}
    where org_id = ${orgId}
      and decision in ('applied', 'rolled_back')
      and target_id is not null
      and target_kind <> 'discovery'
    order by target_kind, target_id, created_at desc`);
  return rows.rows.map((row) => ({ targetKind: row.target_kind, targetId: row.target_id, version: row.id }));
}

/**
 * Records the store versions a run reads. A sub-run copies its root's rows, so
 * every run in one task reads the same versions.
 */
export async function pinRunStoreVersions(orgId: string, runId: string, rootRunId?: string | null): Promise<number> {
  if (rootRunId && rootRunId !== runId) {
    const copied = await db().execute(sql`
      insert into ${run_store_version} (org_id, run_id, target_kind, target_id, version)
      select org_id, ${runId}, target_kind, target_id, version
      from ${run_store_version}
      where org_id = ${orgId} and run_id = ${rootRunId}
      on conflict do nothing`);
    return copied.rowCount ?? 0;
  }
  const versions = await currentStoreVersions(orgId);
  if (versions.length === 0) return 0;
  const inserted = await db()
    .insert(run_store_version)
    .values(
      versions.map((v) => ({
        org_id: orgId,
        run_id: runId,
        target_kind: v.targetKind,
        target_id: v.targetId,
        version: v.version,
      })),
    )
    .onConflictDoNothing()
    .returning({ targetId: run_store_version.target_id });
  return inserted.length;
}

export async function runStoreVersion(runId: string, target: LearnTarget): Promise<string | null> {
  const [row] = await db()
    .select({ version: run_store_version.version })
    .from(run_store_version)
    .where(
      and(
        eq(run_store_version.run_id, runId),
        eq(run_store_version.target_kind, target.kind),
        eq(run_store_version.target_id, target.id),
      ),
    )
    .limit(1);
  return row?.version ?? null;
}

/**
 * The rules a run reads for a target: the pinned version when the store moved
 * on since the run's task started, otherwise the live rules.
 */
export async function readPinnedRules(orgId: string, runId: string, target: LearnTarget): Promise<LearnedRule[]> {
  const live = () => readRulesAt(learnTargetDir(getOrgAgentRoot(orgId), target));
  const pinned = await runStoreVersion(runId, target);
  if (!pinned) return live();
  const current = (await currentStoreVersions(orgId)).find(
    (v) => v.targetKind === target.kind && v.targetId === target.id,
  );
  if (current?.version === pinned) return live();
  const [event] = await db()
    .select({ trace: skill_learn_event.model_trace, decision: skill_learn_event.decision })
    .from(skill_learn_event)
    .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.id, pinned)))
    .limit(1);
  const rules = (event?.trace as { versionRules?: unknown } | undefined)?.versionRules;
  return Array.isArray(rules) ? (rules as LearnedRule[]) : live();
}

export type ConsumedChannelMessage = {
  channelThread: string;
  messageRef: string;
  senderKind: "human" | "agent";
  senderId?: string | null;
  accessScope: string;
  content: string;
};

/** Records channel messages a run consumed; a repeated message reference is ignored. */
export async function recordRunChannelMessages(
  orgId: string,
  runId: string,
  messages: readonly ConsumedChannelMessage[],
): Promise<number> {
  if (messages.length === 0) return 0;
  const inserted = await db()
    .insert(run_channel_message)
    .values(
      messages.map((m) => ({
        org_id: orgId,
        run_id: runId,
        channel_thread: m.channelThread,
        message_ref: m.messageRef,
        sender_kind: m.senderKind,
        sender_id: m.senderId ?? null,
        access_scope: m.accessScope,
        content: m.content,
      })),
    )
    .onConflictDoNothing()
    .returning({ id: run_channel_message.id });
  return inserted.length;
}
