import pg from "pg";
import { readFile, readdir } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { applyMigration } from "./migrate-helpers.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const MIGRATIONS_DIR = resolve(HERE, "..", "..", "..", "db", "migrations");

function configBase() {
  const xdg = process.env.XDG_CONFIG_HOME?.trim();
  return xdg && xdg.length > 0
    ? xdg
    : join(process.env.HOME || homedir(), ".config");
}

async function readLocalConfig() {
  const paths = [
    join(configBase(), "openneko", "config.json"),
    join(configBase(), "neko", "config.json"),
  ];
  for (const path of paths) {
    try {
      const parsed = JSON.parse(await readFile(path, "utf8"));
      if (parsed && typeof parsed === "object") return parsed;
    } catch {
      // Missing or malformed local config; try the next path.
    }
  }
  return {};
}

const localConfig = await readLocalConfig();
const localPg = localConfig.pg && typeof localConfig.pg === "object"
  ? localConfig.pg
  : {};
const sslmode = localPg.sslmode ?? process.env.NEKO_PG_SSLMODE;
const client = new pg.Client({
  host: localPg.host ?? process.env.NEKO_PG_HOST ?? "localhost",
  port: Number(localPg.port ?? process.env.NEKO_PG_PORT ?? 5432),
  user: localPg.user ?? process.env.NEKO_PG_USER ?? "neko",
  password: localPg.password ?? process.env.NEKO_PG_PASSWORD ?? "secret",
  database: localPg.database ?? process.env.NEKO_PG_DATABASE ?? "neko",
  ssl: sslmode === "require" ? { rejectUnauthorized: false } : undefined,
});

await client.connect();
await client.query(
  "CREATE TABLE IF NOT EXISTS schema_migrations (name text primary key, applied_at timestamptz not null default now())",
);

const { rows: appliedRows } = await client.query(
  "SELECT name FROM schema_migrations",
);
const applied = new Set(appliedRows.map((r) => r.name));

const files = (await readdir(MIGRATIONS_DIR))
  .filter((f) => f.endsWith(".sql"))
  .sort();

if (applied.size === 0) {
  const r = await client.query(
    "SELECT to_regclass('public.organization') AS exists",
  );
  if (r.rows[0].exists !== null) {
    console.log(
      "[migrate] existing schema detected without tracking — seeding schema_migrations as fully applied",
    );
    for (const file of files) {
      await client.query(
        "INSERT INTO schema_migrations(name) VALUES($1) ON CONFLICT DO NOTHING",
        [file],
      );
      applied.add(file);
    }
  }
}

let ranCount = 0;
for (const file of files) {
  if (applied.has(file)) continue;
  const sql = await readFile(resolve(MIGRATIONS_DIR, file), "utf8");
  console.log(`[migrate] applying ${file}`);
  try {
    await applyMigration(client, file, sql);
    ranCount += 1;
  } catch (err) {
    console.error(`[migrate] FAILED on ${file}`);
    throw err;
  }
}

console.log(
  `[migrate] done — ${ranCount} new, ${files.length - ranCount} already applied`,
);
await client.end();
