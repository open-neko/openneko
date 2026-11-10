import {
  queryFileTables,
  readFileText,
  searchFiles,
  type FileReadResult,
  type FileSearchResult,
  type FileSqlResult,
  type FilesIdentity,
} from "../files/client";
import { resolveRunOwnerUserId } from "./library";

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
