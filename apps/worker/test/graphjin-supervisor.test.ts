import { spawn } from "node:child_process";
import { chmod, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

const children: Array<ReturnType<typeof spawn>> = [];

afterEach(async () => {
  for (const child of children.splice(0)) {
    child.kill("SIGTERM");
    await new Promise((resolveExit) => child.once("exit", resolveExit));
  }
});

async function waitFor(predicate: () => Promise<boolean>, timeoutMs = 5_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((resolveWait) => setTimeout(resolveWait, 50));
  }
  throw new Error("condition did not become true");
}

describe("GraphJin supervisor", () => {
  it("acknowledges health pings and replaces the child on a restart request", async () => {
    const directory = await mkdtemp(join(tmpdir(), "openneko-graphjin-supervisor-"));
    const binary = join(directory, "graphjin");
    const starts = join(directory, "starts.log");
    await writeFile(binary, `#!/bin/sh
printf 'start\\n' >> "$GRAPHJIN_TEST_STARTS"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`);
    await chmod(binary, 0o755);
    const supervisor = spawn(
      "/bin/sh",
      [resolve(process.cwd(), "../../scripts/graphjin-supervisor.sh"), "serve", "--path", directory],
      {
        cwd: resolve(process.cwd(), "../.."),
        env: {
          ...process.env,
          PATH: `${directory}:${process.env.PATH ?? ""}`,
          GRAPHJIN_TEST_STARTS: starts,
          OPENNEKO_GRAPHJIN_CONFIG_DIR: directory,
        },
        stdio: "ignore",
      },
    );
    children.push(supervisor);
    try {
      await waitFor(async () => (await readFile(starts, "utf8").catch(() => "")).includes("start"));

      await writeFile(join(directory, ".openneko-graphjin-supervisor-ping"), "ping-1");
      await waitFor(async () =>
        (await readFile(join(directory, ".openneko-graphjin-supervisor-ping-ack"), "utf8").catch(() => "")) === "ping-1",
      );

      await writeFile(join(directory, ".openneko-graphjin-restart"), "restart-1");
      await waitFor(async () =>
        (await readFile(join(directory, ".openneko-graphjin-restart-ack"), "utf8").catch(() => "")) === "restart-1",
      );
      await waitFor(async () =>
        (await readFile(starts, "utf8").catch(() => "")).trim().split("\n").length >= 2,
      );
    } finally {
      supervisor.kill("SIGTERM");
      await new Promise((resolveExit) => supervisor.once("exit", resolveExit));
      children.splice(children.indexOf(supervisor), 1);
      await rm(directory, { recursive: true, force: true });
    }
  });

  it("deletes a stale GraphJin agent key file and starts GraphJin without a key", async () => {
    const directory = await mkdtemp(join(tmpdir(), "openneko-graphjin-supervisor-"));
    const binary = join(directory, "graphjin");
    const starts = join(directory, "starts.log");
    await writeFile(binary, `#!/bin/sh
printf '%s\\n' "\${OPENNEKO_GRAPHJIN_AGENT_API_KEY:-none}" >> "$GRAPHJIN_TEST_STARTS"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`);
    await chmod(binary, 0o755);
    const keyFile = join(directory, ".openneko-graphjin-agent-key");
    await writeFile(keyFile, "key-1", { mode: 0o600 });
    const supervisor = spawn(
      "/bin/sh",
      [resolve(process.cwd(), "../../scripts/graphjin-supervisor.sh"), "serve", "--path", directory],
      {
        cwd: resolve(process.cwd(), "../.."),
        env: {
          ...process.env,
          PATH: `${directory}:${process.env.PATH ?? ""}`,
          GRAPHJIN_TEST_STARTS: starts,
          OPENNEKO_GRAPHJIN_CONFIG_DIR: directory,
        },
        stdio: "ignore",
      },
    );
    children.push(supervisor);
    try {
      await waitFor(async () => (await readFile(starts, "utf8").catch(() => "")).trim() === "none");
      await expect(stat(keyFile)).rejects.toThrow(/ENOENT/);
    } finally {
      supervisor.kill("SIGTERM");
      await new Promise((resolveExit) => supervisor.once("exit", resolveExit));
      children.splice(children.indexOf(supervisor), 1);
      await rm(directory, { recursive: true, force: true });
    }
  });
});
