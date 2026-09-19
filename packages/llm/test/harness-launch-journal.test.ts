import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { admitHarnessLaunch } from "../src/work/harness-launch-journal";
import { reapStrandedSandboxes } from "../src/work/sandbox-launcher";

const roots: string[] = [];
async function root() { const path = await mkdtemp(join(tmpdir(), "harness-journal-")); roots.push(path); return path; }
afterEach(async () => { await Promise.all(roots.splice(0).map(path => rm(path, { recursive: true, force: true }))); });

it("admits one concurrent attempt, fails closed before a receipt and replays completion", async () => {
  const path = await root();
  const attempts = await Promise.allSettled([admitHarnessLaunch(path, { input: "q", scope: "a" }), admitHarnessLaunch(path, { input: "q", scope: "a" })]);
  expect(attempts.filter(a => a.status === "fulfilled")).toHaveLength(1);
  await expect(admitHarnessLaunch(path, { input: "q", scope: "a" })).rejects.toThrow("outcome unknown");
  const accepted = attempts.find(a => a.status === "fulfilled") as PromiseFulfilledResult<Awaited<ReturnType<typeof admitHarnessLaunch>>>;
  await accepted.value.complete!({ status: "completed", finalText: "saved evidence" });
  expect((await admitHarnessLaunch(path, { input: "q", scope: "a" })).result?.finalText).toBe("saved evidence");
  await expect(admitHarnessLaunch(path, { input: "q", scope: "revoked" })).rejects.toThrow("conflicts");
  await expect(admitHarnessLaunch(path, { input: "changed", scope: "a" })).rejects.toThrow("conflicts");
});

it("rejects truncated admission and invalid completion receipts", async () => {
  const path = await root();
  await writeFile(join(path, "accepted.json"), "{");
  await expect(admitHarnessLaunch(path, {})).rejects.toThrow("outcome unknown");
  const other = await root(); await admitHarnessLaunch(other, {});
  await writeFile(join(other, "result.json"), JSON.stringify({version:99,result:{status:"completed",finalText:"bad"}}));
  await expect(admitHarnessLaunch(other, {})).rejects.toThrow("invalid result");
});

it("preserves recovery sandboxes while retaining the Hermes restart cleanup", async () => {
  const calls: string[][] = [];
  const removed = await reapStrandedSandboxes(async args => {
    calls.push(args);
    return args.includes("list") ? JSON.stringify([
      {name:"hermes-old",labels:{"openneko.boot":"old"}},
      {name:"harness-interrupted",labels:{"openneko.boot":"old","openneko.recovery":"retain"}},
      {name:"current",labels:{"openneko.boot":"new"}},
    ]) : "";
  }, "test", "new");
  expect(removed).toEqual(["hermes-old"]);
  expect(calls.filter(c=>c.includes("delete"))).toEqual([["sandbox","delete","hermes-old"]]);
});

it.each([false, true])("survives actual host SIGKILL (receipt saved: %s)", async saved => {
  const { spawn } = await import("node:child_process");
  const { once } = await import("node:events");
  const { createRequire } = await import("node:module");
  const { pathToFileURL } = await import("node:url");
  const path = await root();
  const tsx = createRequire(import.meta.url).resolve("tsx", { paths: [join(process.cwd(), "../../apps/worker")] });
  const journal = pathToFileURL(join(process.cwd(), "src/work/harness-launch-journal.ts")).href;
  const child = spawn(process.execPath, ["--import", tsx, "--input-type=module", "-e", `
    import { admitHarnessLaunch } from ${JSON.stringify(journal)};
    const admission = await admitHarnessLaunch(${JSON.stringify(path)}, {input:"same"});
    ${saved ? 'await admission.complete({status:"completed",finalText:"durable read evidence"});' : ''}
    process.send("durable"); setInterval(()=>{},1000);
  `], {stdio:["ignore","ignore","pipe","ipc"]});
  try {
    await once(child, "message");
    const exited = once(child, "exit"); child.kill("SIGKILL"); await exited;
    if (saved) expect((await admitHarnessLaunch(path, {input:"same"})).result?.finalText).toBe("durable read evidence");
    else await expect(admitHarnessLaunch(path, {input:"same"})).rejects.toThrow("outcome unknown");
  } finally { child.kill("SIGKILL"); }
});
