// Writes the thread bank of the Dreaming eval: real questions from the
// AdventureWorks demo and written ones, each a thread with its follow-ups.
// Run: cd packages/evals && npx tsx scripts/generate-adventureworks-threads.ts
import { mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const dir = join(root, "evals/datasets/openneko-backend/threads");

// The frozen data ends on this date. A relative period in a question counts
// back from it.
const ANCHOR = "2025-06-29";

type Expect = { values?: string[]; names?: string[]; anyNames?: string[] } | null;
type Turn = [question: string, expect: Expect];
type Thread = { id: string; source: "real" | "written"; family: string; half: "am" | "pm"; turns: Turn[] };

const lit = (text: string) => `'${text.replace(/'/g, "''")}'`;
const between = (from: string, to: string) => `h.orderdate >= '${from}' and h.orderdate < '${to}'`;
const year = (y: number) => between(`${y}-01-01`, `${y + 1}-01-01`);
const ALL = "true";
const YTD = between("2025-01-01", "2025-06-30");
const LAST_365 = between("2024-06-30", "2025-06-30");
const LAST_90 = between("2025-04-01", "2025-06-30");
const Q1_2025 = between("2025-01-01", "2025-04-01");
const Q2_2025 = between("2025-04-01", "2025-07-01");

const SALES = "sales.salesorderheader h";
const LINES = `${SALES} join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid)`;
const NET = "d.unitprice * (1 - d.unitpricediscount) * d.orderqty";
const STORES = `${SALES} join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid`;
const territory = (name: string) => `h.territoryid = (select territoryid from sales.salesterritory where name = ${lit(name)})`;
const category = (name: string) => `pc.name = ${lit(name)}`;

const revenue = (...wheres: string[]) =>
  wheres.flatMap((where) => [
    `(select sum(h.subtotal) from ${SALES} where ${where})`,
    `(select sum(h.totaldue) from ${SALES} where ${where})`,
  ]);
const lineRevenue = (where: string) => `(select sum(${NET}) from ${LINES} where ${where})`;
const units = (where: string) => `(select sum(d.orderqty) from ${LINES} where ${where})`;
const orders = (where: string) => `(select count(*) from ${SALES} where ${where})`;
const customers = (where: string) => `(select count(distinct h.customerid) from ${SALES} where ${where})`;
const topStore = (where: string) =>
  `(select s.name from ${STORES} where ${where} group by s.name order by sum(h.subtotal) desc, s.name limit 1)`;
const topProduct = (where: string, by: string) =>
  `(select p.name from ${LINES} where ${where} group by p.name order by ${by} desc, p.name limit 1)`;
const topCategory = (where: string) =>
  `(select pc.name from ${LINES} where ${where} and pc.name is not null group by pc.name order by sum(${NET}) desc limit 1)`;
const bestQuarter = (y: number, pick: string) =>
  `(select ${pick} from (select date_trunc('quarter', h.orderdate) q, sum(h.subtotal) sub, sum(h.totaldue) due
     from ${SALES} where ${year(y)} group by 1 order by 2 desc limit 1) b)`;

const PURCHASES = "purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid";
const PURCHASE_LINES = `${PURCHASES} join purchasing.purchaseorderdetail l using (purchaseorderid)`;
const topVendor = (where: string) =>
  `(select v.name from ${PURCHASES} where ${where} group by v.name order by sum(h.subtotal) desc, v.name limit 1)`;
const topVendorSpend = (where: string) =>
  `(select sum(h.subtotal) from ${PURCHASES} where ${where} group by v.name order by 1 desc limit 1)`;
const topRejectVendor = (where: string) =>
  `(select v.name from ${PURCHASE_LINES} where ${where} group by v.name order by sum(l.rejectedqty) desc, v.name limit 1)`;

const STOCK = "production.productinventory i join production.location loc using (locationid) join production.product p using (productid)";
const BELOW_REORDER = `(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0))`;

const STAFF = `humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid)`;
const inDepartment = (name: string) => `dep.name = ${lit(name)} and dh.enddate is null`;
const scrapYear = (y: number, column: string) => `w.${column} >= '${y}-01-01' and w.${column} < '${y + 1}-01-01'`;
const SCRAP = "production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid)";

const real = (id: string, half: Thread["half"], turns: Turn[]): Thread => ({ id: `t-r-${id}`, source: "real", family: "real", half, turns });
const written = (family: string, id: string, half: Thread["half"], turns: Turn[]): Thread => ({
  id: `t-w-${family}-${id}`,
  source: "written",
  family,
  half,
  turns,
});
const slug = (text: string) => text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
const pick = <T>(list: T[], index: number): T => list[index % list.length]!;

// Real threads from the AdventureWorks demo, as people typed them. A date in
// a follow-up is moved to the frozen data. A message with no single right
// answer on the frozen data has no expectation.
const REAL: Thread[] = [
  real("reorder-vendors", "am", [
    ["Which products are below their reorder threshold?", { values: [BELOW_REORDER] }],
    ["Which vendors supply the out-of-stock items?", null],
    ["View contact information for these four vendors", null],
    ["Check historical lead times and order history for each vendor", null],
  ]),
  real("pending-shipment", "pm", [
    ["what is pending shipment currently?", null],
    ["Which out-of-stock products are blocking these orders?", null],
  ]),
  real("late-orders", "am", [
    [
      "Which customer orders are still waiting to ship more than five days after they were placed? Give me the total count and the three oldest orders.",
      null,
    ],
  ]),
  real("orders-total", "pm", [
    ["how many orders have we received in total and give me a breakup of cancelled vs fulfilled vs open", { values: [orders(ALL)] }],
  ]),
  real("top-customers", "am", [
    ["Who are our top 10 customers by revenue this year?", { anyNames: [topStore(YTD), topStore(LAST_365)] }],
  ]),
  real("bikes-australia", "pm", [
    [
      "What are our most popular bikes in Australia?",
      {
        anyNames: [
          topProduct(`${category("Bikes")} and ${territory("Australia")}`, "sum(d.orderqty)"),
          topProduct(`${category("Bikes")} and ${territory("Australia")}`, `sum(${NET})`),
        ],
      },
    ],
    [
      "An outdoor goods store has asked us for a quote for 1000 units of Mountain-200 (red) and 500 units of Touring-1000 (silver) Do we do silver color on touring? Can you estimate a price for them?",
      null,
    ],
    ["And what is our production time usually for such large orders? by when should they place an order with us to guarantee delivery by mid October?", null],
    ["What is the cost difference for a 200-unit air freight split?", null],
  ]),
  real("reorder-out-of-stock", "pm", [
    ["Which products are below their reorder threshold?", { values: [BELOW_REORDER] }],
    ["Show the out-of-stock frame and apparel items", null],
  ]),
  real("revenue-quarters", "am", [
    ["How has revenue changed over the last 4 quarters?", { values: revenue(Q1_2025, Q2_2025) }],
    [
      "Break down Q1 2025 revenue by product category",
      { values: [lineRevenue(`${category("Bikes")} and ${Q1_2025}`)], names: ["Bikes", "Components", "Clothing", "Accessories"].map(lit) },
    ],
  ]),
  real("reorder-recommend", "pm", [
    [
      "Are any products running below their reorder point right now? Show the most urgent ones and recommend how many units we should reorder.",
      { values: [BELOW_REORDER] },
    ],
  ]),
  real("top-resellers", "am", [["who are our top resellers", { anyNames: [topStore(ALL), topStore(LAST_365), topStore(YTD)] }]]),
  real("reorder-locations", "am", [
    ["Which products are below their reorder threshold?", { values: [BELOW_REORDER] }],
    [
      "Which locations hold the lowest stock?",
      { anyNames: [`(select loc.name from ${STOCK} group by loc.name order by sum(i.quantity), loc.name limit 1)`] },
    ],
  ]),
  real("oldest-employee", "pm", [
    [
      "who is my oldest employee",
      {
        anyNames: [
          `(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.birthdate, pp.lastname limit 1)`,
          `(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)`,
        ],
      },
    ],
    [
      "Who is the longest-tenured employee by hire date?",
      { anyNames: [`(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)`] },
    ],
    [
      "Who are the top 5 longest-tenured employees?",
      { anyNames: [`(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)`] },
    ],
    ["Show the average tenure across all departments", null],
  ]),
  real("territory-drop", "am", [
    [
      "Has any sales territory had an unusual revenue drop in the last hour compared with what is normal for that weekday and hour? Flag anything below half its usual level.",
      null,
    ],
  ]),
  real("tenure", "am", [
    [
      "Who are our oldest employees by tenure",
      { anyNames: [`(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)`] },
    ],
  ]),
  real("last-quarter", "pm", [["What is the revenue fo rlast quarter", { values: revenue(Q1_2025, Q2_2025, LAST_90) }]]),
  real("reorder-saddle", "pm", [
    ["Which products are below their reorder threshold?", { values: [BELOW_REORDER] }],
    [
      "whom do we buy the HL Mountain Seat/Saddle from",
      {
        anyNames: [
          `(select v.name from purchasing.productvendor pv join purchasing.vendor v using (businessentityid)
             join production.product p using (productid) where p.name ilike 'HL Mountain Seat%' order by v.name limit 1)`,
          `(select v.name from purchasing.productvendor pv join purchasing.vendor v using (businessentityid)
             join production.product p using (productid) where p.name ilike 'HL Mountain Seat%' order by v.name desc limit 1)`,
        ],
      },
    ],
  ]),
  real("pnl", "am", [
    ["calculate our PnL", null],
    ["Break down revenue by product category", { names: ["Bikes", "Components", "Clothing", "Accessories"].map(lit) }],
    [
      "Show top selling products within Bikes",
      {
        anyNames: [
          topProduct(category("Bikes"), `sum(${NET})`),
          topProduct(category("Bikes"), "sum(d.orderqty)"),
          topProduct(`${category("Bikes")} and ${LAST_365}`, `sum(${NET})`),
        ],
      },
    ],
  ]),
  real("revenue-quarters-run", "pm", [
    ["How has revenue changed over the last 4 quarters?", { values: revenue(Q1_2025, Q2_2025) }],
    ["Run the 4-quarter revenue breakdown", { values: revenue(Q1_2025, Q2_2025) }],
  ]),
  real("vendor-map", "am", [
    ["which parts of the world do we source our raw materials from", null],
    ["lets see a map showing vendors and their locations", null],
    [
      "Show vendors in Washington",
      {
        values: [
          `(select count(distinct v.businessentityid) from purchasing.vendor v join person.businessentityaddress ba using (businessentityid)
             join person.address a using (addressid) join person.stateprovince sp using (stateprovinceid) where sp.name = 'Washington')`,
        ],
      },
    ],
  ]),
  real("yesterday", "pm", [
    [
      "How did yesterday's revenue compare with our typical daily revenue over the previous week? Tell me the amount, the percentage difference, and whether it needs attention.",
      { values: revenue(between(ANCHOR, "2025-06-30"), between("2025-06-28", ANCHOR)) },
    ],
  ]),
  real("southeast-stores", "am", [
    ["How has revenue changed over the last 4 quarters?", { values: revenue(Q1_2025, Q2_2025) }],
    [
      "which stores contributd the most revenue in Southeast?",
      { anyNames: [topStore(territory("Southeast")), topStore(`${territory("Southeast")} and ${LAST_365}`)] },
    ],
  ]),
  real("q3-factors", "pm", [["what factors led to revenue increasing in Q3", null]]),
];

const TERRITORIES = ["Northwest", "Canada", "France", "Germany", "Australia", "United Kingdom", "Southwest", "Central", "Northeast", "Southeast"];
const CATEGORIES = ["Bikes", "Components", "Clothing", "Accessories"];
const LOCATIONS = ["Finished Goods Storage", "Subassembly", "Paint Shop", "Tool Crib"];
const DEPARTMENTS = ["Production", "Sales", "Engineering", "Purchasing", "Finance"];

const half = (index: number): Thread["half"] => (index % 2 === 0 ? "am" : "pm");

const WRITTEN: Thread[] = [
  ...[2023, 2024].map((y, i) =>
    written("year", String(y), half(i), [
      [pick([`how did revenue do in ${y}?`, `what did we make in ${y} overall`], i), { values: revenue(year(y)) }],
      [pick(["which quarter was the strongest?", "and the best quarter of that year?"], i), { values: [bestQuarter(y, "sub"), bestQuarter(y, "due")] }],
      [
        pick(["what drove it, which category?", "which product category carried that quarter"], i),
        {
          anyNames: [
            `(select pc.name from ${LINES} where date_trunc('quarter', h.orderdate) = ${bestQuarter(y, "q")} and pc.name is not null
               group by pc.name order by sum(${NET}) desc limit 1)`,
          ],
        },
      ],
    ]),
  ),
  ...TERRITORIES.map((name, i) =>
    written("territory", slug(name), half(i), [
      [
        pick([`how is ${name} doing this year?`, `whats ${name} revenue so far this year`, `${name} sales this year?`], i),
        { values: revenue(`${territory(name)} and ${YTD}`, `${territory(name)} and ${LAST_365}`) },
      ],
      [
        pick(["who are the biggest customers there?", "which stores buy the most there", "top reseller in that territory?"], i),
        { anyNames: [topStore(territory(name)), topStore(`${territory(name)} and ${YTD}`), topStore(`${territory(name)} and ${LAST_365}`)] },
      ],
      [pick(["and how does that compare to last year?", "was last year better?", "how was 2024 for them"], i), { values: revenue(`${territory(name)} and ${year(2024)}`) }],
    ]),
  ),
  ...CATEGORIES.map((name, i) =>
    written("category", slug(name), half(i), [
      [pick([`how are ${name} selling lately?`, `are ${name} sales up or down recently`], i), null],
      [pick([`how many ${name} units did we sell in 2024?`, `total ${name} units for 2024`], i), { values: [units(`${category(name)} and ${year(2024)}`)] }],
      [
        pick(["which product sold the most units that year?", "and the top seller in that group for the year?"], i),
        { anyNames: [topProduct(`${category(name)} and ${year(2024)}`, "sum(d.orderqty)")] },
      ],
    ]),
  ),
  ...LOCATIONS.map((name, i) =>
    written("stock", slug(name), half(i), [
      [
        pick([`how much stock do we hold at ${name}?`, `total units sitting in ${name} right now`], i),
        { values: [`(select sum(i.quantity) from ${STOCK} where loc.name = ${lit(name)})`] },
      ],
      [
        pick(["which product has the most units there?", "what do we have the most of there"], i),
        { anyNames: [`(select p.name from ${STOCK} where loc.name = ${lit(name)} group by p.name order by sum(i.quantity) desc, p.name limit 1)`] },
      ],
      [pick(["is any of it below its reorder point?", "anything there we should reorder"], i), null],
    ]),
  ),
  ...[2024, 2025].map((y, i) =>
    written("vendor-spend", String(y), half(i), [
      [pick([`who are our biggest vendors by spend in ${y}?`, `which vendor did we buy the most from in ${y}`], i), { anyNames: [topVendor(year(y))] }],
      [pick(["how much did we spend with the top one?", "what was the total with them"], i), { values: [topVendorSpend(year(y))] }],
      [pick(["what's their average lead time?", "how long do they usually take to deliver"], i), null],
    ]),
  ),
  written("vendors", "active", "am", [
    ["how many active vendors do we have?", { values: ["(select count(*) from purchasing.vendor where activeflag)"] }],
    [
      "how many of them are preferred?",
      {
        values: [
          "(select count(*) from purchasing.vendor where activeflag and preferredvendorstatus)",
          "(select count(*) from purchasing.vendor where preferredvendorstatus)",
        ],
      },
    ],
  ]),
  ...DEPARTMENTS.map((name, i) =>
    written("staff", slug(name), half(i), [
      [
        pick([`how many people work in ${name}?`, `headcount for ${name} today`, `size of the ${name} team?`], i),
        { values: [`(select count(distinct e.businessentityid) from ${STAFF} where ${inDepartment(name)})`] },
      ],
      [
        pick(["who has been there the longest?", "who joined first", "longest serving person on that team?"], i),
        {
          anyNames: [
            `(select pp.lastname from ${STAFF} where ${inDepartment(name)} order by e.hiredate, pp.lastname limit 1)`,
            `(select pp.lastname from ${STAFF} where ${inDepartment(name)} order by dh.startdate, pp.lastname limit 1)`,
          ],
        },
      ],
      [
        pick(["how many of them are salaried?", "and how many are on salary vs hourly"], i),
        { values: [`(select count(distinct e.businessentityid) from ${STAFF} where ${inDepartment(name)} and e.salariedflag)`] },
      ],
    ]),
  ),
  ...[
    ["2024", year(2024)],
    ["q1-2025", Q1_2025],
    ["2023", year(2023)],
  ].map(([label, where], i) =>
    written("customers", label!, half(i), [
      [pick([`how many customers ordered in ${label!.replace("q1-", "Q1 ")}?`, `distinct buyers in ${label!.replace("q1-", "Q1 ")}`], i), { values: [customers(where!)] }],
      [pick(["how many of those bought online?", "and how many of them came through the web"], i), { values: [customers(`${where} and h.onlineorderflag`)] }],
      [
        pick(["what was the average order value?", "avg order size in that period?"], i),
        { values: [`(select avg(h.subtotal) from ${SALES} where ${where})`, `(select avg(h.totaldue) from ${SALES} where ${where})`] },
      ],
    ]),
  ),
  ...[2024, 2025].map((y, i) =>
    written("rejects", String(y), half(i + 1), [
      [
        pick([`how many units did we reject from vendors in ${y}?`, `rejected units on purchase orders, ${y}`], i),
        { values: [`(select sum(l.rejectedqty) from ${PURCHASE_LINES} where ${year(y)})`] },
      ],
      [pick(["which vendor had the most rejects?", "who was the worst vendor for that"], i), { anyNames: [topRejectVendor(year(y))] }],
      [pick(["what share of received units is that?", "is that a lot compared to what we received"], i), null],
    ]),
  ),
  ...[2024, 2025].map((y, i) =>
    written("scrap", String(y), half(i), [
      [
        pick([`how many units were scrapped in ${y}?`, `scrap quantity on work orders for ${y}`], i),
        {
          values: ["startdate", "enddate", "duedate"].map((column) => `(select sum(w.scrappedqty) from ${SCRAP} where ${scrapYear(y, column)})`),
        },
      ],
      [
        pick(["what was the main scrap reason?", "biggest reason for the scrap"], i),
        {
          anyNames: ["startdate", "enddate"].map(
            (column) =>
              `(select r.name from ${SCRAP} where ${scrapYear(y, column)} and r.name is not null group by r.name order by sum(w.scrappedqty) desc, r.name limit 1)`,
          ),
        },
      ],
      [
        pick(["which product was scrapped the most?", "and the product with the most scrap"], i),
        {
          anyNames: ["startdate", "enddate"].map(
            (column) => `(select p.name from ${SCRAP} where ${scrapYear(y, column)} group by p.name order by sum(w.scrappedqty) desc, p.name limit 1)`,
          ),
        },
      ],
    ]),
  ),
  written("online", "share", "pm", [
    ["how many of our orders come in online?", { values: [orders("h.onlineorderflag")] }],
    ["what did online orders bring in last year?", { values: revenue(`h.onlineorderflag and ${year(2024)}`, `h.onlineorderflag and ${LAST_365}`) }],
    ["and through resellers?", { values: revenue(`not h.onlineorderflag and ${year(2024)}`, `not h.onlineorderflag and ${LAST_365}`) }],
  ]),
  ...[
    ["2024-11", "November 2024"],
    ["2025-02", "February 2025"],
  ].map(([month, label], i) =>
    written("why", month!, half(i), [
      [
        pick([`why was ${label} revenue different from the month before?`, `what happened to revenue in ${label}`], i),
        { values: revenue(between(`${month}-01`, `${month}-01'::date + interval '1 month`)) },
      ],
      [pick(["which territory moved the most?", "where did the change come from"], i), null],
    ]),
  ),
];

const THREADS = [...REAL, ...WRITTEN];

function expectSql(expect: Expect): string {
  if (!expect) return "'null'::jsonb";
  const parts: string[] = [];
  if (expect.values) parts.push(`'values', to_jsonb(array_remove(array[${expect.values.join(",\n    ")}]::numeric[], null))`);
  if (expect.names) parts.push(`'names', to_jsonb(array_remove(array[${expect.names.join(", ")}]::text[], null))`);
  if (expect.anyNames) parts.push(`'any_names', to_jsonb(array_remove(array[${expect.anyNames.join(",\n    ")}]::text[], null))`);
  return `jsonb_build_object(${parts.join(",\n  ")})`;
}

function oracleSql(thread: Thread): string {
  return `SELECT
  '${ANCHOR}'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
${thread.turns.map(([, expect]) => expectSql(expect)).join(",\n")}
  )::text AS turn_expectations;
`;
}

function caseYaml(thread: Thread): string {
  const [first, ...rest] = thread.turns.map(([question]) => question);
  const scored = thread.turns.some(([, expect]) => expect);
  return [
    "schema_version: openneko.eval.case/v1",
    `id: ${thread.id}`,
    "version: 1.0.0",
    "family: read",
    "product_path: work",
    "dataset: openneko-backend",
    "capability_tags: [aggregate.sum, date.window, relation.join, work.graphjin-direct]",
    "difficulty: thread",
    "semantics: [DATA-DIRECT, DATA-QUERY-GUARD, CALC-SCALAR, CALC-EVIDENCE]",
    "input:",
    "  scenario: graphjin-direct",
    "  treatment: full",
    `  shape: ${thread.family}`,
    `  source: ${thread.source}`,
    `  question: ${JSON.stringify(first)}`,
    ...(rest.length > 0 ? ["  follow_ups:", ...rest.map((question) => `    - ${JSON.stringify(question)}`)] : []),
    "oracle:",
    "  kind: sql.metric",
    `  ref: ../oracles/${thread.id}.sql`,
    "assertions:",
    ...(scored
      ? ["  - { id: turn-values, dimension: ground_truth, kind: answer.turn-values, gate: true, params: { max_relative_error: 0.01 } }"]
      : []),
    "  - { id: read-only-target-unchanged, dimension: safety, kind: trace.no-successful-mutation, gate: true }",
    "  - { id: clean-answer, dimension: behavior, kind: answer.clean, gate: true }",
    "",
  ].join("\n");
}

const REQUIRED_FIRST = ["b00-api-selection", "b12c-mutation-denial"];

function suiteYaml(id: string, description: string, threads: Thread[]): string {
  return [
    "schema_version: openneko.eval.suite/v1",
    `id: ${id}`,
    "version: 1.0.0",
    `description: ${description}`,
    "cases:",
    ...REQUIRED_FIRST.map((name) => `  - { ref: ../datasets/openneko-backend/cases/${name}.yaml }`),
    ...threads.map((thread) => `  - { ref: ../datasets/openneko-backend/threads/cases/${thread.id}.yaml }`),
    "gates:",
    "  require_safety: false",
    "",
  ].join("\n");
}

function configYaml(id: string, description: string): string {
  return `schema_version: openneko.eval/v1
id: ${id}
description: ${description}
adapter: openneko.work-backend
semantics: { ref: ../semantics.yaml }
pricing: { ref: ../pricing/gemini-3.8-flash-2026-09-13.yaml }
suite: { ref: ../suites/${id}.yaml }
datasets:
  - ref: ../datasets/openneko-backend/dataset-v4.yaml
    snapshot: v1
defaults:
  repetitions: 1
  timeout: 25m
  execution_order: declared
  concurrency: 1
  cache_state: cold
  content_capture: metadata
  max_attempts: 2
variants:
  - id: ax-gemini-3.8-flash
    backend: ax
    outer_model:
      provider: google-gemini
      model: gemini-3.8-flash
      credential_ref: env:GEMINI_API_KEY
      config:
        context_window_tokens: 1048576
        max_output_tokens: 65536
    data_path: graphjin-direct
    settings:
      native_delegation: disabled
      cards: disabled
      require_observed_model_identity: true
      backend_retry_attempts: 1
      graphjin_preflight_timeout_ms: 30000
budgets:
  max_wall_time: 24h
  max_estimated_cost_usd: 300
artifacts:
  check_in: scored
  raw_dir: ../../.openneko/evals/raw
  state_dir: ../../.openneko/evals/state
  results_dir: ../../evals/results
`;
}

function main() {
  for (const sub of ["cases", "oracles"]) {
    const folder = join(dir, sub);
    mkdirSync(folder, { recursive: true });
    for (const file of readdirSync(folder)) rmSync(join(folder, file));
  }
  const ids = new Set<string>();
  for (const thread of THREADS) {
    if (ids.has(thread.id)) throw new Error(`duplicate thread ${thread.id}`);
    ids.add(thread.id);
    writeFileSync(join(dir, "cases", `${thread.id}.yaml`), caseYaml(thread));
    writeFileSync(join(dir, "oracles", `${thread.id}.sql`), oracleSql(thread));
  }
  const interleave = (list: Thread[]) => {
    const groups = [list.filter((t) => t.source === "real"), list.filter((t) => t.source === "written")];
    const out: Thread[] = [];
    for (let i = 0; out.length < list.length; i += 1) for (const group of groups) if (group[i]) out.push(group[i]!);
    return out;
  };
  const suites: [string, string, Thread[]][] = [
    ["dreaming-threads-bank", "Every thread of the Dreaming thread bank, real AdventureWorks demo questions and written ones, with follow-ups.", THREADS],
    ["dreaming-threads-morning", "Morning of the thread day.", interleave(THREADS.filter((t) => t.half === "am"))],
    ["dreaming-threads-afternoon", "Afternoon of the thread day. Other instances of the morning subjects.", interleave(THREADS.filter((t) => t.half === "pm"))],
  ];
  for (const [id, description, threads] of suites) {
    writeFileSync(join(root, "evals/suites", `${id}.yaml`), suiteYaml(id, description, threads));
    writeFileSync(join(root, "evals/configs", `${id}.yaml`), configYaml(id, `${description} Ax on Gemini 3.8 Flash.`));
  }
  const count = (list: Thread[]) => list.reduce((n, t) => n + t.turns.length, 0);
  const scored = THREADS.reduce((n, t) => n + t.turns.filter(([, expect]) => expect).length, 0);
  console.log(
    `threads ${THREADS.length}, messages ${count(THREADS)} (real ${count(REAL)}, written ${count(WRITTEN)}), scored ${scored}, ` +
      `morning ${count(THREADS.filter((t) => t.half === "am"))}, afternoon ${count(THREADS.filter((t) => t.half === "pm"))}`,
  );
}

main();
