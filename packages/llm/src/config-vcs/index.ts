import { existsSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import { git } from "./git-shell";
import { withRepoLock } from "./lock";

/**
 * CV0 — config-vcs: invisible auto-versioning. One git repo per org whose
 * working tree IS the org agents dir (~/.config/openneko/agents/orgs/<id>);
 * only the config artifacts (skills/, workflows/, memory/) are tracked.
 * Commits are system-authored — no personal identity in git, per
 * docs/DATA_LIFECYCLE.md §3. Failures are the caller's to swallow: a
 * versioning hiccup must never fail the artifact write itself.
 */

const TRACKED_IGNORE = `# config-vcs (CV0): version config artifacts only.
*
!.gitignore
!skills/
!skills/**
!workflows/
!workflows/**
!memory/
!memory/**
`;

export type ConfigCommit = {
  sha: string;
  message: string;
  date: string;
};

export async function ensureConfigRepo(workspaceRoot: string): Promise<void> {
  const root = resolve(workspaceRoot);
  await withRepoLock(root, async () => {
    if (!existsSync(join(root, ".git"))) {
      await mkdir(root, { recursive: true });
      await git(root, ["init", "--initial-branch=main"]);
    }
    const ignorePath = join(root, ".gitignore");
    if (!existsSync(ignorePath)) {
      await writeFile(ignorePath, TRACKED_IGNORE, "utf8");
      await git(root, ["add", ".gitignore"]);
      await git(root, ["commit", "-m", "Initialize config versioning"]);
    }
  });
}

/**
 * Stage + commit the given paths (relative to the workspace root). Returns
 * the new HEAD sha, or null when there was nothing to commit.
 */
export async function commitConfigChange(opts: {
  workspaceRoot: string;
  paths: string[];
  message: string;
}): Promise<string | null> {
  const root = resolve(opts.workspaceRoot);
  await ensureConfigRepo(root);
  return withRepoLock(root, async () => {
    await git(root, ["add", "-A", "--", ...opts.paths]);
    const { stdout: status } = await git(root, [
      "status",
      "--porcelain",
      "--",
      ...opts.paths,
    ]);
    if (!status.trim()) return null;
    await git(root, ["commit", "-m", opts.message]);
    const { stdout } = await git(root, ["rev-parse", "HEAD"]);
    return stdout.trim();
  });
}

/** Plain-English history for the whole repo or one artifact path. */
export async function listConfigHistory(
  workspaceRoot: string,
  path?: string,
  limit = 50,
): Promise<ConfigCommit[]> {
  const root = resolve(workspaceRoot);
  if (!existsSync(join(root, ".git"))) return [];
  const args = [
    "log",
    `--max-count=${limit}`,
    "--pretty=format:%H%x09%cI%x09%s",
  ];
  if (path) args.push("--", path);
  const { stdout } = await git(root, args).catch(() => ({ stdout: "" }));
  return stdout
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const [sha, date, ...rest] = line.split("\t");
      return { sha: sha ?? "", date: date ?? "", message: rest.join("\t") };
    });
}

/**
 * Restore one artifact path to its state at `sha`, recorded as a NEW
 * commit (history only moves forward; no resets).
 */
export async function restoreConfigPath(opts: {
  workspaceRoot: string;
  sha: string;
  path: string;
}): Promise<string | null> {
  const root = resolve(opts.workspaceRoot);
  if (!existsSync(join(root, ".git"))) return null;
  return withRepoLock(root, async () => {
    await git(root, ["checkout", opts.sha, "--", opts.path]);
    await git(root, ["add", "-A", "--", opts.path]);
    const { stdout: status } = await git(root, [
      "status",
      "--porcelain",
      "--",
      opts.path,
    ]);
    if (!status.trim()) return null;
    await git(root, [
      "commit",
      "-m",
      `Restored ${basename(opts.path)} to ${opts.sha.slice(0, 8)}`,
    ]);
    const { stdout } = await git(root, ["rev-parse", "HEAD"]);
    return stdout.trim();
  });
}

/**
 * Best-effort hook used by artifact writers: commit + record the team
 * ref pointer. Never throws — a versioning failure must not fail the
 * write that triggered it.
 */
export async function recordConfigChange(opts: {
  workspaceRoot: string;
  orgId?: string;
  paths: string[];
  message: string;
}): Promise<void> {
  try {
    const sha = await commitConfigChange(opts);
    if (!sha) return;
    const orgId = opts.orgId ?? basename(resolve(opts.workspaceRoot));
    await upsertTeamRef(orgId, sha);
  } catch (err) {
    console.warn(
      `[config-vcs] versioning failed (write succeeded): ${err instanceof Error ? err.message : err}`,
    );
  }
}

async function upsertTeamRef(orgId: string, sha: string): Promise<void> {
  try {
    const { db, config_ref, sql } = await import("@neko/db");
    await db()
      .insert(config_ref)
      .values({ org_id: orgId, scope: "team", commit_sha: sha })
      .onConflictDoUpdate({
        target: [config_ref.org_id, config_ref.scope, config_ref.user_id],
        set: { commit_sha: sha, updated_at: new Date() },
      });
    void sql;
  } catch (err) {
    console.warn(
      `[config-vcs] config_ref update failed (commit persisted): ${err instanceof Error ? err.message : err}`,
    );
  }
}
