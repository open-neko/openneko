// Pure helpers for the migration runner so the wrapping behavior can be
// tested without spinning up a real DB.
//
// Files ending with `_no_tx.sql` opt out of the wrapping transaction —
// needed for statements like REINDEX DATABASE / REINDEX SCHEMA / VACUUM
// that Postgres rejects inside a transaction block.

export function shouldRunInTransaction(filename) {
  if (typeof filename !== "string" || filename.length === 0) return true;
  return !filename.endsWith("_no_tx.sql");
}

// Runs one migration's SQL with the right tx wrapping for its filename.
// `client.query` is the only injected dependency — keeps the helper
// trivially mockable without dragging pg in.
export async function applyMigration(client, filename, sql) {
  const inTx = shouldRunInTransaction(filename);
  if (inTx) {
    await client.query("BEGIN");
    try {
      await client.query(sql);
      await client.query("INSERT INTO schema_migrations(name) VALUES($1)", [filename]);
      await client.query("COMMIT");
    } catch (err) {
      await client.query("ROLLBACK");
      throw err;
    }
  } else {
    await client.query(sql);
    await client.query("INSERT INTO schema_migrations(name) VALUES($1)", [filename]);
  }
}
