import {
  queryFileTables,
  readFileText,
  searchFiles,
  type FileReadResult,
  type FileSearchResult,
  type FileSqlResult,
  type FilesIdentity,
} from "../files/client";
import { and, db, eq, work_run, work_thread, workflow_definition, workflow_run } from "@neko/db";

export type FileSearchForRunArgs = { orgId: string; runId: string | null; query: string; limit?: number };
export type FileReadForRunArgs = { orgId: string; runId: string | null; path: string; offset?: number; maxChars?: number };
export type FileSqlForRunArgs = { orgId: string; runId: string | null; sql: string };

// The folder belongs to the run's owner. The agent never names the user.
async function identityForRun(orgId: string, runId: string | null): Promise<FilesIdentity> {
  const userId = runId ? await resolveRunOwnerUserId(orgId, runId) : null;
  if (!userId) {
    throw new Error("This run has no owner, so it has no private files to read.");
  }
  return { orgId, userId };
}

export async function searchFilesForRun(args: FileSearchForRunArgs): Promise<FileSearchResult> {
  return searchFiles(await identityForRun(args.orgId, args.runId), args.query, args.limit);
}

export async function readFileForRun(args: FileReadForRunArgs): Promise<FileReadResult> {
  return readFileText(await identityForRun(args.orgId, args.runId), args.path, {
    ...(args.offset !== undefined ? { offset: args.offset } : {}),
    ...(args.maxChars !== undefined ? { maxChars: args.maxChars } : {}),
  });
}

export async function queryFileTablesForRun(args: FileSqlForRunArgs): Promise<FileSqlResult> {
  return queryFileTables(await identityForRun(args.orgId, args.runId), args.sql);
}

export async function resolveRunOwnerUserId(orgId: string, runId: string): Promise<string | null> {
  const rows = await db()
    .select({ owner: work_thread.created_by_user_id })
    .from(work_run)
    .innerJoin(work_thread, eq(work_run.thread_id, work_thread.id))
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (rows[0]?.owner) return rows[0].owner;
  // API and scheduled runs have no thread creator; they act as the routine's owner.
  const [routine] = await db()
    .select({ owner: workflow_definition.owner_user_id })
    .from(workflow_run)
    .innerJoin(workflow_definition, eq(workflow_run.workflow_id, workflow_definition.id))
    .where(and(eq(workflow_run.org_id, orgId), eq(workflow_run.work_run_id, runId)))
    .limit(1);
  return routine?.owner || null;
}
