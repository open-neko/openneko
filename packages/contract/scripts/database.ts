import pg from "pg";

const [action, adminUrl, name] = process.argv.slice(2);
if (!adminUrl || !name || !/^[a-z][a-z0-9_]*$/.test(name) || (action !== "create" && action !== "drop")) {
  console.error("usage: database.ts create|drop <postgres admin URL> <database name>");
  process.exit(2);
}
const client = new pg.Client({ connectionString: adminUrl });
await client.connect();
await client.query(`drop database if exists ${name} with (force)`);
if (action === "create") await client.query(`create database ${name}`);
await client.end();
