import { mkdir, writeFile } from "node:fs/promises";
import { chmodSync } from "node:fs";
import { join } from "node:path";
import { mintGraphjinToken, GRAPHJIN_TOKEN_TTL_SECONDS } from "./token";

/**
 * GJ4 — per-run GraphJin client auth for the CLI path. The GraphJin CLI
 * reads `$XDG_CONFIG_HOME/graphjin/client.json` ({server, token}) and
 * sends `Authorization: Bearer <token>` on every MCP request. We write a
 * per-run client.json inside the run workspace and the graphjin-guard
 * wrapper pins XDG_CONFIG_HOME at it — so each run's CLI calls carry
 * that run's actor token (K1 snapshot) and nothing else on the host can
 * read another run's token. Legacy mode (data_source.auth_mode='none')
 * skips all of this.
 */
export type GraphjinClientAuth = {
  /** The XDG dir the guard pins (contains graphjin/client.json). */
  xdgConfigHome: string;
  token: string;
};

export async function provisionGraphjinClientAuth(opts: {
  /** Per-run dir to hold the config (e.g. the workspace runRoot). */
  runRoot: string;
  /** GraphJin MCP server URL (data_source.mcp_url). */
  serverUrl: string;
  orgId: string;
  userId: string | null;
  role: "admin" | "member" | "service";
}): Promise<GraphjinClientAuth> {
  const xdgConfigHome = join(opts.runRoot, "gj-auth");
  const dir = join(xdgConfigHome, "graphjin");
  await mkdir(dir, { recursive: true });
  const token = mintGraphjinToken({
    orgId: opts.orgId,
    userId: opts.userId,
    role: opts.role,
  });
  const path = join(dir, "client.json");
  await writeFile(
    path,
    JSON.stringify(
      {
        server: opts.serverUrl,
        token,
        expires_at: new Date(
          Date.now() + GRAPHJIN_TOKEN_TTL_SECONDS * 1000,
        ).toISOString(),
        subject: opts.userId ?? "service",
      },
      null,
      2,
    ),
    "utf8",
  );
  try {
    chmodSync(path, 0o600);
  } catch {
    // best-effort (non-POSIX)
  }
  return { xdgConfigHome, token };
}
