/**
 * Postgres pool config builder.
 *
 * The connection details come from `~/.config/neko/config.json`
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
 * Production deploys pre-populate `~/.config/neko/config.json` via the
 * Docker entrypoint, which materializes it from NEKO_PG_* env vars.
 *
 * `sslmode: "require"` in the config enables TLS with `rejectUnauthorized:
 * false` (matches Cloud SQL public-IP usage with its self-signed CA).
 */

import type { PoolConfig } from "pg";
import { readLocalConfig } from "./local-config";

const DEFAULT_PG = {
  host: "localhost",
  port: 5432,
  user: "neko",
  password: "secret",
  database: "neko",
} as const;

export function buildPoolConfig(overrides: { database?: string } = {}): PoolConfig {
  const cfg = readLocalConfig().pg ?? {};
  const poolCfg: PoolConfig = {
    host: cfg.host ?? DEFAULT_PG.host,
    port: cfg.port ?? DEFAULT_PG.port,
    user: cfg.user ?? DEFAULT_PG.user,
    password: cfg.password ?? DEFAULT_PG.password,
    database: overrides.database ?? cfg.database ?? DEFAULT_PG.database,
    max: 10,
  };

  if (cfg.sslmode === "require") {
    poolCfg.ssl = { rejectUnauthorized: false };
  }

  return poolCfg;
}
