import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { git } from "./git-shell";
import { ensureConfigRepo } from "./index";
import { withRepoLock } from "./lock";

/**
 * CV4 — personal config layers as `user/<id>` refs in the org config
 * repo. The refs are written with a temporary index (never the working
 * tree, which stays the team checkout) and are never merged into main —
 * so offboarding can drop a person's entire git footprint by deleting
 * one ref and pruning (docs/DATA_LIFECYCLE.md §2/§3).
 */

export type UserConfigFile = { path: string; content: string };

/** Git-ref-safe branch name for a user id; no emails/names in refs. */
export function userConfigRef(userId: string): string {
  const slug = userId.replace(/[^a-zA-Z0-9_-]/g, "-").replace(/^-+|-+$/g, "");
  const safe =
    slug === userId
      ? slug
      : `${slug}-${createHash("sha256").update(userId).digest("hex").slice(0, 8)}`;
  return `refs/heads/user/${safe || "anon"}`;
}

/**
 * Commit a tree to the user's ref without touching the working tree.
 * mode 'replace' makes the provided files the entire tree (snapshot);
 * 'overlay' starts from the ref's current tree and lays the files on
 * top (single-artifact save). Returns the new sha, or null when the
 * tree is unchanged.
 */
export async function commitToUserRef(opts: {
  workspaceRoot: string;
  userId: string;
  files: UserConfigFile[];
  message: string;
  mode?: "replace" | "overlay";
}): Promise<string | null> {
  const root = resolve(opts.workspaceRoot);
  await ensureConfigRepo(root);
  return withRepoLock(root, async () => {
    const ref = userConfigRef(opts.userId);
    const parent = await git(root, ["rev-parse", "--verify", "--quiet", ref])
      .then((r) => r.stdout.trim() || null)
      .catch(() => null);
    const tmp = await mkdtemp(join(tmpdir(), "neko-gitidx-"));
    const env = { GIT_INDEX_FILE: join(tmp, "index") };
    try {
      if (opts.mode === "overlay" && parent) {
        await git(root, ["read-tree", parent], { env });
      } else {
        await git(root, ["read-tree", "--empty"], { env });
      }
      for (const file of opts.files) {
        const { stdout: blob } = await git(
          root,
          ["hash-object", "-w", "--stdin"],
          { env, input: file.content },
        );
        await git(
          root,
          [
            "update-index",
            "--add",
            "--cacheinfo",
            `100644,${blob.trim()},${file.path}`,
          ],
          { env },
        );
      }
      const { stdout: treeOut } = await git(root, ["write-tree"], { env });
      const tree = treeOut.trim();
      if (parent) {
        const { stdout: parentTree } = await git(root, [
          "rev-parse",
          `${parent}^{tree}`,
        ]);
        if (parentTree.trim() === tree) return null;
      }
      const { stdout: shaOut } = await git(root, [
        "commit-tree",
        tree,
        ...(parent ? ["-p", parent] : []),
        "-m",
        opts.message,
      ]);
      const sha = shaOut.trim();
      await git(root, ["update-ref", ref, sha]);
      return sha;
    } finally {
      await rm(tmp, { recursive: true, force: true });
    }
  });
}

/**
 * GDPR offboarding (DATA_LIFECYCLE §2): drop the member's whole git
 * footprint — delete their ref, expire reflogs, prune unreachable
 * blobs — and remove their DB pointers.
 */
export async function dropUserConfigData(opts: {
  orgId: string;
  userId: string;
  workspaceRoot: string;
}): Promise<void> {
  const root = resolve(opts.workspaceRoot);
  if (existsSync(join(root, ".git"))) {
    await withRepoLock(root, async () => {
      await git(root, ["update-ref", "-d", userConfigRef(opts.userId)]).catch(
        () => {},
      );
      await git(root, ["reflog", "expire", "--expire=now", "--all"]).catch(
        () => {},
      );
      await git(root, ["gc", "--prune=now", "--quiet"]).catch(() => {});
    });
  }
  const { db, config_ref, memory_fork, and, eq } = await import("@neko/db");
  await db()
    .delete(config_ref)
    .where(
      and(
        eq(config_ref.org_id, opts.orgId),
        eq(config_ref.scope, "user"),
        eq(config_ref.user_id, opts.userId),
      ),
    );
  await db()
    .delete(memory_fork)
    .where(
      and(
        eq(memory_fork.org_id, opts.orgId),
        eq(memory_fork.user_id, opts.userId),
      ),
    );
}
