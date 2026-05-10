import pg from "pg";
import { readFile, readdir } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const MIGRATIONS_DIR = resolve(HERE, "..", "..", "..", "db", "migrations");

const sslmode = process.env.NEKO_PG_SSLMODE;
const client = new pg.Client({
  host: process.env.NEKO_PG_HOST ?? "localhost",
  port: Number(process.env.NEKO_PG_PORT ?? 5432),
  user: process.env.NEKO_PG_USER ?? "neko",
  password: process.env.NEKO_PG_PASSWORD ?? "secret",
  database: process.env.NEKO_PG_DATABASE ?? "neko",
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
  await client.query("BEGIN");
  try {
    await client.query(sql);
    await client.query("INSERT INTO schema_migrations(name) VALUES($1)", [file]);
    await client.query("COMMIT");
    ranCount += 1;
  } catch (err) {
    await client.query("ROLLBACK");
    console.error(`[migrate] FAILED on ${file}`);
    throw err;
  }
}

console.log(
  `[migrate] done — ${ranCount} new, ${files.length - ranCount} already applied`,
);
await client.end();
