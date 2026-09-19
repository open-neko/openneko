import { createHash, randomUUID } from "node:crypto";
import { mkdir, open, rename, rm } from "node:fs/promises";
import { join } from "node:path";
import type { AgentRunResult } from "../agent-backend";

const LIMIT = 8 * 1024 * 1024;

async function syncDirectory(root: string) {
  const directory = await open(root, "r");
  try { await directory.sync(); } finally { await directory.close(); }
}

async function readRecord(path: string): Promise<any> {
  const file = await open(path, "r");
  try {
    if ((await file.stat()).size > LIMIT) throw new Error("Harness journal exceeds limit");
    return JSON.parse(await file.readFile("utf8"));
  } finally { await file.close(); }
}

/** Host-only admission fence. An unresolved launch is never automatically replayed.
 * The directory must be outside the workspace uploaded to the sandbox.
 * ponytail: one attempt per run; explicit reconciliation is required after interruption.
 */
export async function admitHarnessLaunch(root: string, identity: unknown) {
  await mkdir(root, { recursive: true, mode: 0o700 });
  const fingerprint = createHash("sha256").update(JSON.stringify(identity)).digest("hex");
  const accepted = join(root, "accepted.json");
  let file;
  try { file = await open(accepted, "wx", 0o600); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    let prior;
    try { prior = await readRecord(accepted); }
    catch { throw new Error("Harness launch outcome unknown: invalid admission record; reconciliation required"); }
    if (prior?.version !== 1 || prior.fingerprint !== fingerprint) {
      throw new Error("Harness launch conflicts with accepted input or authorization scope");
    }
    let receipt;
    try { receipt = await readRecord(join(root, "result.json")); }
    catch { throw new Error("Harness launch outcome unknown: prior attempt requires reconciliation; automatic relaunch disabled"); }
    if (receipt?.version !== 1 || receipt.fingerprint !== fingerprint ||
      !["completed", "failed", "cancelled"].includes(receipt.result?.status) || typeof receipt.result?.finalText !== "string") {
      throw new Error("Harness launch outcome unknown: invalid result receipt");
    }
    return { result: receipt.result as AgentRunResult, complete: undefined };
  }
  try { await file.writeFile(JSON.stringify({ version: 1, fingerprint })); await file.sync(); }
  finally { await file.close(); }
  await syncDirectory(root);
  return {
    result: undefined,
    complete: async (result: AgentRunResult) => {
      const data = JSON.stringify({ version: 1, fingerprint, result });
      if (Buffer.byteLength(data) > LIMIT) throw new Error("Harness result receipt exceeds limit");
      const temporary = join(root, `.result-${randomUUID()}`);
      try {
        const output = await open(temporary, "wx", 0o600);
        try { await output.writeFile(data); await output.sync(); } finally { await output.close(); }
        await rename(temporary, join(root, "result.json"));
        await syncDirectory(root);
      } finally { await rm(temporary, { force: true }); }
    },
  };
}
