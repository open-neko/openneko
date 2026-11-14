import pg from "pg";

const thread = "c0a7ac70-0000-4000-8000-000000000011";
const run = "c0a7ac70-0000-4000-8000-000000000021";
const link = "https://example.com/a/very/long/path/that/does/not/break/anywhere/inventory_reservation_report_2026_11_14";
const answer = [
  "## Stock by warehouse",
  "",
  "| Warehouse | SKU | On hand | Reserved | Available | Days of cover | Reorder point | Supplier |",
  "|---|---|---|---|---|---|---|---|",
  "| North distribution centre | SKU-1001-BLUE-XL | 49,310 | 12,004 | 37,306 | 18.4 | 20,000 | Acme Industrial Supplies Ltd |",
  "| South | SKU-1002 | 2 | 0 | 2 | 0.1 | 50 | Beta |",
  "",
  "The table name is `inventory_source_item_reservation_history_by_warehouse_and_day`.",
  "",
  "```sql",
  "select sku, sum(quantity) as on_hand, sum(reserved) as reserved from inventory_source_item group by sku order by on_hand desc;",
  "```",
  "",
  `See ${link} for detail.`,
].join("\n");

const client = new pg.Client({ connectionString: process.argv[2] });
await client.connect();
const { rows } = await client.query("select id from organization limit 1");
const org = rows[0].id;
await client.query(
  "insert into customer_profile (org_id, version, is_current, business_profile) values ($1, 1, true, 'A test store')",
  [org],
);
await client.query(
  `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
   ($1, $2, null, 'user', $4, now() - interval '2 minutes'),
   ($1, $2, $3, 'assistant', $5, now() - interval '1 minute')`,
  [org, thread, run, `Show me ${link} and SKU_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789`, answer],
);
await client.end();
