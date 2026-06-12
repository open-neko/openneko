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
// config.json stores the rotated password ENCRYPTED (enc:v1, see
// writeLocalConfig) — using it raw fails auth on any deployment where the
// wizard's password step ran. Decrypt like every other reader.
if (typeof localPg.password === "string" && localPg.password.startsWith("enc:")) {
  const { maybeDecryptSecret } = await import("@neko/secret-crypt");
  localPg.password = maybeDecryptSecret(localPg.password);
}
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
    `insert into data_source (org_id, kind, graphql_url, subscription_url, mcp_url, label)
     values ($1, 'graphjin', $2, $3, $4, 'AdventureWorks')`,
    [
      orgId,
      `${DEFAULT_GRAPHJIN_ROOT}/api/v1/graphql`,
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

// ─── Bundled workflows + policy (L2 trial seed) ────────────────────────
// Pre-author a small set of cron workflows so the trial workspace
// produces real outputs against the AdventureWorks data within minutes
// of first run. Idempotent: only inserts when no workflows exist yet
// for the org, so a re-seed never clobbers operator-authored workflows.
const wfRows = await client.query(
  "select id from workflow_definition where org_id = $1 limit 1",
  [orgId],
);
if (wfRows.rowCount === 0) {
  const trialWorkflows = [
    {
      name: "Daily Revenue Health Check",
      description: "Snapshot of yesterday's revenue vs trailing 7-day average. Lands on the Briefing each morning.",
      goal: "Once per day, query AdventureWorks sales data via the connected GraphJin source. Compute (a) yesterday's total revenue across all territories from sales.salesorderheader.subtotal, and (b) the average daily revenue over the prior 7 days. Emit one workflow_output with kind=finding, mood=good if yesterday is within 10% of the avg, mood=watch if yesterday is 10–25% below, mood=act if >25% below. The output title should name yesterday's revenue and the delta; the body should be 1–2 sentences explaining the comparison.",
      cron: "0 9 * * *",
      cron_timezone: "UTC",
    },
    {
      name: "Revenue Drop Alert",
      description: "Hourly territory-level revenue drop check. Surfaces sharp declines.",
      goal: "Once per hour, query AdventureWorks via GraphJin: for each territoryid in sales.salesorderheader, compute the current trailing-hour revenue and compare it to the same hour-of-week averaged over the prior 4 weeks. Emit a workflow_output with kind=finding, mood=act when any territory's current-hour revenue is below 50% of its baseline, scope=territory:<id>. Skip silently if no territory crosses the threshold. When mood=act, also emit a neko_action_request proposing a send_webhook notification to slack:#revenue-alerts with a one-line summary.",
      cron: "0 * * * *",
      cron_timezone: "UTC",
    },
    {
      name: "Slow-Ship Operations",
      description: "Daily check for orders stuck in pending status past SLA.",
      goal: "Once per day, query AdventureWorks via GraphJin to find rows in sales.salesorderheader where status=1 (pending) and orderdate is more than 5 days ago. Emit one workflow_output with kind=finding, mood=watch when the count is between 1 and 10, mood=act when >10. Title: the count of slow-shipping orders. Body: a short list of the oldest 3 orderids with their orderdate.",
      cron: "30 8 * * *",
      cron_timezone: "UTC",
    },
    {
      name: "Stock Reorder Watch",
      description: "Reacts to inventory rows that drop below the product's reorder point. Subscription-triggered (no cron); fires per low-stock row.",
      goal: "Triggered by a source_change subscription on production.productinventory when a row's quantity falls below the related product's reorderpoint. The trigger_payload includes table, primary_key={productid,locationid}, and snapshot. Query AdventureWorks via GraphJin to fetch the product name, current quantity, reorderpoint, and listprice. Emit one workflow_output with kind=recommendation, mood=act, scope='inventory', topic='reorder_point' summarizing the gap and recommending a reorder quantity. Do NOT mutate productinventory or productorders here — the responder reads only.",
      cron: null,
      cron_timezone: null,
    },
  ];
  for (const wf of trialWorkflows) {
    const hasCron = wf.cron !== null && wf.cron !== undefined;
    await client.query(
      `insert into workflow_definition (
         org_id, name, description, goal, cron, cron_timezone,
         cron_enabled, enabled, status
       ) values ($1, $2, $3, $4, $5, $6, $7, true, 'active')
       on conflict (org_id, owner_user_id, name) do nothing`,
      [
        orgId,
        wf.name,
        wf.description,
        wf.goal,
        wf.cron,
        wf.cron_timezone ?? "UTC",
        hasCron,
      ],
    );
  }
  console.log(`[seed-adventureworks] inserted ${trialWorkflows.length} trial workflows`);

  const stockWatchRows = await client.query(
    "select id from workflow_definition where org_id = $1 and name = 'Stock Reorder Watch' limit 1",
    [orgId],
  );
  const stockWatchWorkflowId = stockWatchRows.rows[0]?.id;
  if (stockWatchWorkflowId) {
    await client.query(
      `insert into subscription (
         org_id, workflow_id, source_kind, filter, enabled,
         max_concurrent_runs, idempotency_key_template
       ) values ($1, $2, 'source_change', $3::jsonb, true, 5, $4)`,
      [
        orgId,
        stockWatchWorkflowId,
        JSON.stringify({
          table: "productinventory",
          where: { quantity: { lt: { col: "product.reorderpoint" } } },
          primary_key: ["productid", "locationid"],
          version_column: "modifieddate",
          select: ["quantity"],
        }),
        "reorder-{primary_key}",
      ],
    );
    console.log("[seed-adventureworks] wired Stock Reorder Watch subscription");
  }
} else {
  console.log("[seed-adventureworks] workflows already exist; leaving them unchanged");
}

const policyRows = await client.query(
  "select id from action_policy where org_id = $1 and name = 'revenue_alerts_auto' limit 1",
  [orgId],
);
if (policyRows.rowCount === 0) {
  await client.query(
    `insert into action_policy (
       org_id, name, description,
       applies_to_kinds, applies_to_scopes,
       mode, risk_threshold_auto_approve,
       allowed_targets, limits, priority, enabled
     ) values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, true)`,
    [
      orgId,
      "revenue_alerts_auto",
      "Auto-approve revenue alert notifications to slack:#revenue-alerts at risk medium or below.",
      ["send_webhook"],
      ["external"],
      "auto_approve",
      "medium",
      JSON.stringify(["slack:#revenue-alerts"]),
      JSON.stringify({}),
      800,
    ],
  );
  console.log("[seed-adventureworks] inserted revenue_alerts_auto policy");
} else {
  console.log("[seed-adventureworks] revenue_alerts_auto policy already exists; leaving it unchanged");
}

await client.end();
