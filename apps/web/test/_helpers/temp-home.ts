/**
 * Temp $HOME for host-provision tests.
 *
 * provisionHostConfig writes to ~/.config/graphjin/client.json,
 * ~/Library/Application Support/graphjin/client.json (on macOS), and
 * ~/.hermes/{config.yaml,.env}. Tests must NOT touch the developer's
 * real home dir, so we override HOME (and HERMES_HOME for explicit
 * isolation) for the duration of the test.
 */

import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

export type TempHome = {
  path: string;
  hermesPath: string;
  cleanup: () => Promise<void>;
};

export async function makeTempHome(): Promise<TempHome> {
  const path = await mkdtemp(join(tmpdir(), "neko-test-home-"));
  const hermesPath = join(path, ".hermes");
  const prevHome = process.env.HOME;
  const prevHermesHome = process.env.HERMES_HOME;
  process.env.HOME = path;
  process.env.HERMES_HOME = hermesPath;

  return {
    path,
    hermesPath,
    cleanup: async () => {
      process.env.HOME = prevHome;
      if (prevHermesHome === undefined) delete process.env.HERMES_HOME;
      else process.env.HERMES_HOME = prevHermesHome;
      await rm(path, { recursive: true, force: true });
    },
  };
}
