import { randomUUID } from "node:crypto";
import { readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

/**
 * Ask the GraphJin supervisor to replace its child process, then wait until
 * the new process answers on endpoint.
 */
export async function requestGraphjinRestart(configFile: string, endpoint: string): Promise<void> {
  const directory = dirname(configFile);
  const requestFile = join(directory, ".openneko-graphjin-restart");
  const acknowledgementFile = join(directory, ".openneko-graphjin-restart-ack");
  const token = randomUUID();
  const temporary = `${requestFile}.${token}.tmp`;
  await writeFile(temporary, token, { mode: 0o666 });
  await rename(temporary, requestFile);

  const deadline = Date.now() + 45_000;
  while (Date.now() < deadline) {
    const acknowledged = await readFile(acknowledgementFile, "utf8").catch(() => "");
    if (acknowledged.trim() === token) {
      try {
        await fetch(endpoint, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ query: "query OpenNekoRestartReady { __typename }" }),
          signal: AbortSignal.timeout(2_000),
        });
        return;
      } catch {
        // The supervisor acknowledged the replacement child; wait until its
        // listener is reachable.
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(
    "graphjin_restart_required: the GraphJin supervisor did not acknowledge the restart",
  );
}
