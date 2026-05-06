/**
 * Postgres connection-string builder.
 *
 * No env vars. The connection details come from `~/.config/neko/config.json`
 * (written by /setup) merged with hardcoded defaults that match what the
 * docker compose stack ships with:
 *
 *   user      = "neko"
 *   password  = "secret"      ← initial / unchanged
 *   host      = "localhost"
 *   port      = 5432
 *   database  = "neko"
 *
 * On first boot the config file doesn't exist; the app connects with the
 * defaults. The /setup wizard's "Set DB password" step calls
 * `ALTER USER neko WITH PASSWORD '<new>'` and writes the new password to
 * the config file. From then on the file's value takes precedence.
 *
 * Production deploys pre-populate `~/.config/neko/config.json` via init container
 * / volume mount / secrets-manager — there is no env override path.
 */

import { readLocalConfig } from "./local-config";

const DEFAULT_PG = {
  host: "localhost",
  port: 5432,
  user: "neko",
  password: "secret",
  database: "neko",
} as const;

export function buildConnectionString(): string {
  const cfg = readLocalConfig().pg ?? {};
  const host = cfg.host ?? DEFAULT_PG.host;
  const port = cfg.port ?? DEFAULT_PG.port;
  const user = cfg.user ?? DEFAULT_PG.user;
  const password = cfg.password ?? DEFAULT_PG.password;
  const database = cfg.database ?? DEFAULT_PG.database;
  return `postgres://${user}:${encodeURIComponent(password)}@${host}:${port}/${database}`;
}
