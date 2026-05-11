import pg from "pg";
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";

const DEFAULT_ORG_ID = "adventureworks";
const DEFAULT_ORG_NAME = "AdventureWorks Cycles";
const DEFAULT_COMPANY_NOTE =
  "We design and manufacture bicycles and accessories sold to specialty retailers and direct to consumers across North America, Europe, and Australia.";
const DEFAULT_FISCAL_YEAR_START_MONTH = 7;
const DEFAULT_ACTIVE_SEATS = ["CEO", "CFO", "COO"];
const DEFAULT_PRIORITIES = [
  "Defend wholesale margins",
  "Grow DTC in Europe",
];
const DEFAULT_GRAPHJIN_ROOT = "http://graphjin:8080";

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

const existingOrg = await client.query(
  "select id, name from organization order by created_at asc limit 1",
);
let orgId = existingOrg.rows[0]?.id;
if (!orgId) {
  orgId = DEFAULT_ORG_ID;
  await client.query(
    "insert into organization (id, name) values ($1, $2) on conflict (id) do nothing",
    [orgId, DEFAULT_ORG_NAME],
  );
  console.log(`[seed-adventureworks] created organization ${orgId}`);
} else if (existingOrg.rows[0]?.name === "My Workspace") {
  await client.query(
    "update organization set name = $2, updated_at = now() where id = $1",
    [orgId, DEFAULT_ORG_NAME],
  );
  console.log(`[seed-adventureworks] renamed default organization to ${DEFAULT_ORG_NAME}`);
} else {
  console.log(`[seed-adventureworks] keeping existing organization ${orgId}`);
}

const sourceRows = await client.query(
  "select id from data_source where org_id = $1 limit 1",
  [orgId],
);
if (sourceRows.rowCount === 0) {
  await client.query(
    `insert into data_source (org_id, kind, graphql_url, mcp_url, label)
     values ($1, 'graphjin', $2, $3, 'AdventureWorks')`,
    [
      orgId,
      `${DEFAULT_GRAPHJIN_ROOT}/api/v1/graphql`,
      `${DEFAULT_GRAPHJIN_ROOT}/api/v1/mcp`,
    ],
  );
  console.log("[seed-adventureworks] inserted GraphJin data source");
} else {
  console.log("[seed-adventureworks] data source already exists; leaving it unchanged");
}

const wizardRows = await client.query(
  "select org_id from onboarding_wizard where org_id = $1 limit 1",
  [orgId],
);
if (wizardRows.rowCount === 0) {
  await client.query(
    `insert into onboarding_wizard (
       org_id,
       company_note,
       fiscal_year_start_month,
       active_seats,
       priorities,
       step
     )
     values ($1, $2, $3, $4, $5, 'company')`,
    [
      orgId,
      DEFAULT_COMPANY_NOTE,
      DEFAULT_FISCAL_YEAR_START_MONTH,
      DEFAULT_ACTIVE_SEATS,
      DEFAULT_PRIORITIES,
    ],
  );
  console.log("[seed-adventureworks] inserted onboarding defaults");
} else {
  console.log("[seed-adventureworks] onboarding defaults already exist; leaving them unchanged");
}

await client.end();
