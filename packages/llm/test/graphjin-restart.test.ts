import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { requestGraphjinRestart } from "../src/graphjin/restart";

describe("requestGraphjinRestart", () => {
  afterEach(() => vi.restoreAllMocks());

  it("waits past the 503 that openneko serve answers while GraphJin is down", async () => {
    const dir = await mkdtemp(join(tmpdir(), "gj-restart-"));
    const configFile = join(dir, "agentic.yml");
    const statuses = [503, 503, 200];
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async () => new Response("{}", { status: statuses.shift() ?? 200 }));
    const pending = requestGraphjinRestart(configFile, "http://graphjin:8080/api/v1/graphql");
    const ack = join(dir, ".openneko-graphjin-restart-ack");
    for (let i = 0; i < 20; i++) {
      const token = await readFile(join(dir, ".openneko-graphjin-restart"), "utf8").catch(() => "");
      if (token) {
        await writeFile(ack, token);
        break;
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    await pending;
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(statuses).toEqual([]);
  });
});
