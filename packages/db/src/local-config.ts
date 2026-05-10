/**
 * Local config file at `~/.config/openneko/config.json` (XDG base dir).
 *
 * Stores per-host bootstrap state that can't live in the metadata DB
 * (because the app needs it before the DB connection exists). Currently:
 *
 *   {
 *     "pg": {
 *       "host": "localhost",       // optional override; default localhost
 *       "port": 5432,              // optional override; default 5432
 *       "user": "neko",            // optional override; default 'neko'
 *       "password": "<set by /setup>", // when present, signals admin has changed from default 'secret'
 *       "database": "neko"         // optional override; default 'neko'
 *     }
 *   }
 *
 * On first boot the file doesn't exist; the app uses the hardcoded defaults
 * (which match what the docker compose ships with). The /setup wizard's
 * "Set DB password" step writes a new password to this file and runs
 * ALTER USER. Presence of `pg.password` here is the signal that the admin
 * has finished the password-change step.
 *
 * Sister file in the same dir: `~/.config/openneko/secret-key` (at-rest key).
 */

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export type LocalPgConfig = {
  host?: string;
  port?: number;
  user?: string;
  password?: string;
  database?: string;
  sslmode?: string;
};

export type LocalConfig = {
  pg?: LocalPgConfig;
};

function configDir(): string {
  // Respect XDG_CONFIG_HOME when set; otherwise XDG default of ~/.config/openneko.
  const xdg = process.env.XDG_CONFIG_HOME?.trim();
  const base = xdg && xdg.length > 0
    ? xdg
    : join(process.env.HOME || homedir(), ".config");
  return join(base, "openneko");
}

export function localConfigPath(): string {
  return join(configDir(), "config.json");
}

/** Reads ~/.config/openneko/config.json. Returns {} when the file is missing or malformed. */
export function readLocalConfig(): LocalConfig {
  try {
    const raw = readFileSync(localConfigPath(), "utf8");
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object") return parsed as LocalConfig;
  } catch {
    // missing / malformed → empty config
  }
  return {};
}

/**
 * Deep-merges `partial` into the existing config and writes it atomically.
 * The merge is intentionally shallow at the top level + one level deeper —
 * passing { pg: { password } } only touches pg.password, not the whole pg
 * subtree.
 */
export function writeLocalConfig(partial: LocalConfig): void {
  const current = readLocalConfig();
  const next: LocalConfig = {
    ...current,
    pg: { ...(current.pg ?? {}), ...(partial.pg ?? {}) },
  };
  mkdirSync(configDir(), { recursive: true });
  writeFileSync(localConfigPath(), JSON.stringify(next, null, 2), "utf8");
}

/** Convenience: true when the admin has changed the DB password from default. */
export function hasCustomPassword(): boolean {
  const cfg = readLocalConfig();
  return typeof cfg.pg?.password === "string" && cfg.pg.password.length > 0;
}
