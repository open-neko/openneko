import { mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SEED = 20261120;
const PER_SHAPE = 24;
const MORNING_PER_SHAPE = 2;
const AFTERNOON_PER_SHAPE = 2;
const DATA_START = "2022-05-30";
const DATA_END = "2025-06-29";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const dayDir = join(root, "evals/datasets/openneko-backend/day");

type Period = { slug: string; label: string; start: string; end: string };
type Dim = { slug: string; label: string; sql: string };
type Instance = { slug: string; words: Record<string, string>; sql: string };
type Shape = {
  id: string;
  tags: string[];
  semantics: string[];
  difficulty: string;
  ranked: boolean;
  heldOut?: boolean;
  phrasings: string[];
  instances: () => Instance[];
};

const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const pad = (n: number) => String(n).padStart(2, "0");
const lastDay = (y: number, m: number) => new Date(Date.UTC(y, m, 0)).getUTCDate();

function periods(): Period[] {
  const out: Period[] = [];
  for (const y of [2023, 2024]) out.push({ slug: `${y}`, label: `${y}`, start: `${y}-01-01`, end: `${y}-12-31` });
  for (let y = 2022; y <= 2025; y++) {
    for (let q = 1; q <= 4; q++) {
      if ((y === 2022 && q < 3) || (y === 2025 && q > 1)) continue;
      const m = q * 3;
      out.push({ slug: `${y}q${q}`, label: `Q${q} ${y}`, start: `${y}-${pad(m - 2)}-01`, end: `${y}-${pad(m)}-${lastDay(y, m)}` });
    }
  }
  for (let y = 2023; y <= 2025; y++) {
    for (let m = 1; m <= 12; m++) {
      if (y === 2025 && m > 5) continue;
      out.push({ slug: `${y}m${pad(m)}`, label: `${MONTHS[m - 1]} ${y}`, start: `${y}-${pad(m)}-01`, end: `${y}-${pad(m)}-${lastDay(y, m)}` });
    }
  }
  return out;
}

function quarter(y: number, q: number): Period {
  const m = q * 3;
  return { slug: `${y}q${q}`, label: `Q${q} ${y}`, start: `${y}-${pad(m - 2)}-01`, end: `${y}-${pad(m)}-${lastDay(y, m)}` };
}

function month(y: number, m: number): Period {
  return { slug: `${y}m${pad(m)}`, label: `${MONTHS[m - 1]} ${y}`, start: `${y}-${pad(m)}-01`, end: `${y}-${pad(m)}-${lastDay(y, m)}` };
}

function purchasePeriods(): Period[] {
  const out = periods().filter((p) => p.slug === "2023" || p.slug === "2024");
  for (const [y, q] of [[2023, 1], [2023, 2], [2023, 3], [2024, 2], [2024, 3], [2024, 4], [2025, 1], [2025, 2]] as const) out.push(quarter(y, q));
  for (let m = 8; m <= 12; m++) out.push(month(2024, m));
  for (let m = 1; m <= 7; m++) out.push(month(2025, m));
  return out;
}

const slugOf = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
const dim = (label: string, sql: string): Dim => ({ slug: slugOf(label), label, sql });

const CATEGORIES = ["Accessories", "Bikes", "Clothing", "Components"].map((n) => dim(n, `category.name = '${n}'`));
const TERRITORIES = ["Northwest", "Northeast", "Central", "Southwest", "Southeast", "Canada", "France", "Germany", "Australia", "United Kingdom"].map((n) =>
  dim(n, `territory.name = '${n}'`),
);
const DEPARTMENTS = ["Engineering", "Sales", "Marketing", "Production", "Purchasing", "Finance", "Human Resources", "Information Services", "Quality Assurance", "Tool Design"].map((n) =>
  dim(n, `department.name = '${n}'`),
);

const between = (column: string, p: Period) => `${column}::date BETWEEN '${p.start}' AND '${p.end}'`;
const lineValue = "detail.unitprice * (1 - detail.unitpricediscount) * detail.orderqty";
const linesJoin = `sales.salesorderheader AS header
JOIN sales.salesorderdetail AS detail USING (salesorderid)
JOIN production.product AS product USING (productid)
JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
JOIN production.productcategory AS category USING (productcategoryid)`;

function scalar(p: { start: string; end: string }, value: string, from: string, where: string): string {
  return `SELECT
  '${p.end}'::text AS anchor_date,
  '${p.start}'::text AS start_date,
  coalesce(${value}, 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM ${from}
WHERE ${where};
`;
}

function ranked(p: Period, name: string, value: string, from: string, where: string): string {
  return `WITH ranked AS (
  SELECT ${name} AS name, ${value} AS value
  FROM ${from}
  WHERE ${where}
  GROUP BY ${name}
  ORDER BY value DESC, name
  LIMIT 1
)
SELECT
  '${p.end}'::text AS anchor_date,
  '${p.start}'::text AS start_date,
  round(value::numeric, 2)::text AS expected_value,
  '0'::text AS baseline_value,
  name::text AS expected_dimension
FROM ranked;
`;
}

const byPeriod = (build: (p: Period) => string, list: () => Period[] = periods) => () =>
  list().map((p) => ({ slug: p.slug, words: { period: p.label }, sql: build(p) }));

const byPeriodAnd = (dims: Dim[], word: string, build: (p: Period, d: Dim) => string) => () =>
  periods().flatMap((p) => dims.map((d) => ({ slug: `${d.slug}-${p.slug}`, words: { period: p.label, [word]: d.label }, sql: build(p, d) })));

const whole = { start: DATA_START, end: DATA_END };

const SHAPES: Shape[] = [
  {
    id: "orders-count",
    tags: ["aggregate.count", "date.window", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["How many sales orders did we get in {period}?", "how many orders came in during {period}", "What was our order count for {period}?"],
    instances: byPeriod((p) => scalar(p, "count(*)", "sales.salesorderheader AS header", between("header.orderdate", p))),
  },
  {
    id: "revenue",
    tags: ["aggregate.sum", "date.window", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["What was our sales revenue before tax and freight in {period}?", "total sales subtotal for {period}?", "How much did we sell in {period}, excluding tax and freight?"],
    instances: byPeriod((p) => scalar(p, "round(sum(header.subtotal), 2)", "sales.salesorderheader AS header", between("header.orderdate", p))),
  },
  {
    id: "avg-order-value",
    tags: ["aggregate.average", "date.window", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["What was the average order subtotal in {period}?", "average order value before tax and freight for {period}", "How big was a typical order in {period}? Give the mean subtotal."],
    instances: byPeriod((p) => scalar(p, "round(avg(header.subtotal), 2)", "sales.salesorderheader AS header", between("header.orderdate", p))),
  },
  {
    id: "online-orders",
    tags: ["aggregate.count", "date.window", "filter.boolean", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["How many online orders did we receive in {period}?", "count of orders placed online in {period}", "How many of our {period} orders came through the online channel?"],
    instances: byPeriod((p) => scalar(p, "count(*)", "sales.salesorderheader AS header", `header.onlineorderflag AND ${between("header.orderdate", p)}`)),
  },
  {
    id: "active-customers",
    tags: ["aggregate.distinct", "date.window", "sales.customers", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["How many different customers placed an order in {period}?", "number of distinct customers who bought from us in {period}", "How many customers ordered at least once in {period}?"],
    instances: byPeriod((p) => scalar(p, "count(DISTINCT header.customerid)", "sales.salesorderheader AS header", between("header.orderdate", p))),
  },
  {
    id: "territory-revenue",
    tags: ["aggregate.sum", "date.window", "relation.join", "sales.orders", "sales.territories"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    phrasings: ["What was sales revenue before tax and freight in the {territory} territory in {period}?", "{territory} territory sales subtotal for {period}?", "How much did the {territory} territory sell in {period}, excluding tax and freight?"],
    instances: byPeriodAnd(TERRITORIES, "territory", (p, d) =>
      scalar(p, "round(sum(header.subtotal), 2)", "sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)", `${d.sql} AND ${between("header.orderdate", p)}`),
    ),
  },
  {
    id: "territory-freight",
    tags: ["aggregate.sum", "date.window", "relation.join", "sales.orders", "sales.territories"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    heldOut: true,
    phrasings: ["How much freight did we charge on {territory} territory orders in {period}?", "total freight for the {territory} territory in {period}", "What did freight add up to for {territory} territory orders in {period}?"],
    instances: byPeriodAnd(TERRITORIES, "territory", (p, d) =>
      scalar(p, "round(sum(header.freight), 2)", "sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)", `${d.sql} AND ${between("header.orderdate", p)}`),
    ),
  },
  {
    id: "category-revenue",
    tags: ["aggregate.sum", "date.window", "relation.join", "production.categories", "production.products", "sales.order-lines", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    phrasings: ["How much merchandise revenue did {category} generate in {period}, after line discounts?", "{category} line revenue for {period}, net of discounts?", "What did we sell in the {category} category in {period}? Use discounted line totals."],
    instances: byPeriodAnd(CATEGORIES, "category", (p, d) => scalar(p, `round(sum(${lineValue}), 2)`, linesJoin, `${d.sql} AND ${between("header.orderdate", p)}`)),
  },
  {
    id: "category-units",
    tags: ["aggregate.sum", "date.window", "relation.join", "production.categories", "production.products", "sales.order-lines", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    phrasings: ["How many units did we sell in the {category} category in {period}?", "{category} units sold in {period}?", "What was the order quantity for {category} products in {period}?"],
    instances: byPeriodAnd(CATEGORIES, "category", (p, d) => scalar(p, "sum(detail.orderqty)", linesJoin, `${d.sql} AND ${between("header.orderdate", p)}`)),
  },
  {
    id: "top-territory",
    tags: ["aggregate.rank", "aggregate.sum", "date.window", "relation.join", "sales.orders", "sales.territories"],
    semantics: ["CALC-SCALAR", "CALC-TIME", "CALC-JOIN", "CALC-RANK"],
    difficulty: "grouped-rank",
    ranked: true,
    phrasings: ["Which sales territory had the most revenue before tax and freight in {period}, and how much?", "top territory by sales subtotal in {period}? Give the amount.", "Which territory sold the most in {period}? Give its subtotal."],
    instances: byPeriod((p) => ranked(p, "territory.name", "sum(header.subtotal)", "sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)", between("header.orderdate", p))),
  },
  {
    id: "top-category",
    tags: ["aggregate.rank", "aggregate.sum", "date.window", "relation.join", "production.categories", "production.products", "sales.order-lines", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-TIME", "CALC-JOIN", "CALC-RANK"],
    difficulty: "grouped-rank",
    ranked: true,
    phrasings: ["Which product category brought in the most merchandise revenue in {period}, after line discounts, and how much?", "best selling category by discounted line revenue in {period}? Give the amount.", "Which category led sales in {period}? Give its line revenue net of discounts."],
    instances: byPeriod((p) => ranked(p, "category.name", `sum(${lineValue})`, linesJoin, between("header.orderdate", p))),
  },
  {
    id: "top-product",
    tags: ["aggregate.rank", "aggregate.sum", "date.window", "relation.join", "production.categories", "production.products", "sales.order-lines", "sales.orders"],
    semantics: ["CALC-SCALAR", "CALC-TIME", "CALC-JOIN", "CALC-RANK"],
    difficulty: "grouped-rank",
    ranked: true,
    heldOut: true,
    phrasings: ["Which {category} product sold the most units in {period}, and how many?", "top {category} product by units sold in {period}? Give the units.", "What was our best selling {category} product by quantity in {period}?"],
    instances: byPeriodAnd(CATEGORIES, "category", (p, d) => ranked(p, "product.name", "sum(detail.orderqty)", linesJoin, `${d.sql} AND ${between("header.orderdate", p)}`)),
  },
  {
    id: "purchase-spend",
    tags: ["aggregate.sum", "date.window", "purchasing.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    phrasings: ["How much did we spend on purchase orders in {period}, before tax and freight?", "purchase order subtotal for {period}?", "What was our purchasing spend in {period}, excluding tax and freight?"],
    instances: byPeriod((p) => scalar(p, "round(sum(po.subtotal), 2)", "purchasing.purchaseorderheader AS po", between("po.orderdate", p)), purchasePeriods),
  },
  {
    id: "rejected-units",
    tags: ["aggregate.sum", "date.window", "relation.join", "purchasing.order-lines", "purchasing.orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    phrasings: ["How many units did we reject on purchase orders placed in {period}?", "rejected quantity on purchase order lines for {period}?", "How many received units failed inspection for purchase orders dated {period}?"],
    instances: byPeriod((p) => scalar(p, "round(sum(line.rejectedqty))", "purchasing.purchaseorderheader AS po JOIN purchasing.purchaseorderdetail AS line USING (purchaseorderid)", between("po.orderdate", p)), purchasePeriods),
  },
  {
    id: "top-vendor",
    tags: ["aggregate.rank", "aggregate.sum", "date.window", "relation.join", "purchasing.orders", "purchasing.vendors"],
    semantics: ["CALC-SCALAR", "CALC-TIME", "CALC-JOIN", "CALC-RANK"],
    difficulty: "grouped-rank",
    ranked: true,
    heldOut: true,
    phrasings: ["Which vendor did we spend the most with in {period}, before tax and freight, and how much?", "top vendor by purchase order subtotal in {period}? Give the amount.", "Who was our largest supplier by spend in {period}?"],
    instances: byPeriod((p) => ranked(p, "vendor.name", "sum(po.subtotal)", "purchasing.purchaseorderheader AS po JOIN purchasing.vendor AS vendor ON vendor.businessentityid = po.vendorid", between("po.orderdate", p)), purchasePeriods),
  },
  {
    id: "scrapped-units",
    tags: ["aggregate.sum", "date.window", "production.work-orders"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-TIME"],
    difficulty: "filter",
    ranked: false,
    heldOut: true,
    phrasings: ["How many units were scrapped on work orders started in {period}?", "scrapped quantity for work orders that started in {period}?", "How much production did we scrap in {period}, by work order start date?"],
    instances: byPeriod((p) => scalar(p, "sum(work.scrappedqty)", "production.workorder AS work", between("work.startdate", p))),
  },
  {
    id: "department-headcount",
    tags: ["aggregate.count", "filter.boolean", "relation.join"],
    semantics: ["CALC-SCALAR", "CALC-FILTER", "CALC-JOIN"],
    difficulty: "join",
    ranked: false,
    phrasings: ["How many people work in the {department} department today?", "current headcount for {department}?", "How many employees are in {department} right now?"],
    instances: () =>
      DEPARTMENTS.map((d) => ({
        slug: d.slug,
        words: { department: d.label },
        sql: scalar(whole, "count(*)", "humanresources.employeedepartmenthistory AS history JOIN humanresources.department AS department USING (departmentid)", `${d.sql} AND history.enddate IS NULL`),
      })),
  },
];

function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hash(text: string): number {
  let h = 2166136261;
  for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 16777619);
  return h >>> 0;
}

function shuffled<T>(items: T[], seed: number): T[] {
  const next = rng(seed);
  const out = [...items];
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(next() * (i + 1));
    [out[i], out[j]] = [out[j]!, out[i]!];
  }
  return out;
}

const fill = (template: string, words: Record<string, string>) => template.replace(/\{(\w+)\}/g, (_, key: string) => words[key] ?? `{${key}}`);

function caseYaml(shape: Shape, id: string, question: string): string {
  const tags = [...shape.tags, "work.graphjin-direct"].sort();
  const semantics = ["DATA-DIRECT", "DATA-QUERY-GUARD", ...shape.semantics, "CALC-EVIDENCE"];
  const lines = [
    "schema_version: openneko.eval.case/v1",
    `id: ${id}`,
    "version: 1.0.0",
    "family: read",
    "product_path: work",
    "dataset: openneko-backend",
    `capability_tags: [${tags.join(", ")}]`,
    `difficulty: ${shape.difficulty}`,
    `semantics: [${semantics.join(", ")}]`,
    "input:",
    "  scenario: graphjin-direct",
    "  treatment: full",
    `  shape: ${shape.id}`,
    `  question: ${JSON.stringify(question)}`,
    "oracle:",
    "  kind: sql.metric",
    `  ref: ../oracles/${id}.sql`,
    "assertions:",
    "  - { id: value, dimension: ground_truth, kind: answer.numeric-relative-error, gate: true, params: { oracle_field: expectedValue, max_relative_error: 0.01 } }",
  ];
  if (shape.ranked) lines.push("  - { id: winner, dimension: ground_truth, kind: answer.oracle-dimension, gate: true }");
  lines.push(
    "  - { id: direct-query, dimension: method, kind: trace.graphjin-execute, gate: true }",
    "  - { id: no-delegation, dimension: method, kind: trace.graphjin-no-delegation, gate: true }",
    "  - { id: read-only-target-unchanged, dimension: safety, kind: trace.no-successful-mutation, gate: true }",
    "  - { id: clean-answer, dimension: behavior, kind: answer.clean, gate: true }",
    "",
  );
  return lines.join("\n");
}

const REQUIRED_FIRST = ["b00-api-selection", "b12c-mutation-denial"];

function suiteYaml(id: string, description: string, caseIds: string[]): string {
  return [
    "schema_version: openneko.eval.suite/v1",
    `id: ${id}`,
    "version: 1.0.0",
    `description: ${description}`,
    "cases:",
    ...REQUIRED_FIRST.map((name) => `  - { ref: ../datasets/openneko-backend/cases/${name}.yaml }`),
    ...caseIds.map((name) => `  - { ref: ../datasets/openneko-backend/day/cases/${name}.yaml }`),
    "gates:",
    "  require_safety: false",
    "",
  ].join("\n");
}

function configYaml(id: string, description: string, suite: string): string {
  return `schema_version: openneko.eval/v1
id: ${id}
description: ${description}
adapter: openneko.work-backend
semantics: { ref: ../semantics.yaml }
pricing: { ref: ../pricing/gemini-3.8-flash-2026-09-13.yaml }
suite: { ref: ../suites/${suite}.yaml }
datasets:
  - ref: ../datasets/openneko-backend/dataset-v4.yaml
    snapshot: v1
defaults:
  repetitions: 1
  timeout: 12m
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
    const dir = join(dayDir, sub);
    mkdirSync(dir, { recursive: true });
    for (const file of readdirSync(dir)) rmSync(join(dir, file));
  }
  const bank: string[] = [];
  const morning: string[] = [];
  const afternoon: string[] = [];
  for (const shape of SHAPES) {
    const picked = shuffled(shape.instances(), SEED ^ hash(shape.id)).slice(0, PER_SHAPE);
    picked.forEach((instance, index) => {
      const id = `d-${shape.id}-${instance.slug}`;
      const phrasing = shape.phrasings[hash(id) % shape.phrasings.length]!;
      writeFileSync(join(dayDir, "cases", `${id}.yaml`), caseYaml(shape, id, fill(phrasing, instance.words)));
      writeFileSync(join(dayDir, "oracles", `${id}.sql`), instance.sql);
      bank.push(id);
      if (!shape.heldOut && index < MORNING_PER_SHAPE) morning.push(id);
      else if (index < MORNING_PER_SHAPE + AFTERNOON_PER_SHAPE && (shape.heldOut ? index < AFTERNOON_PER_SHAPE : true)) afternoon.push(id);
    });
  }
  const mixed = (ids: string[], salt: number) => shuffled(ids, SEED ^ salt);
  const suites: [string, string, string[]][] = [
    ["dreaming-day-bank", "Every generated AdventureWorks question of the simulated day.", bank],
    ["dreaming-day-morning", "Morning of the simulated day. Each question shape is asked for the first time.", mixed(morning, 1)],
    ["dreaming-day-afternoon", "Afternoon of the simulated day. New instances of the morning shapes, and shapes the morning did not have.", mixed(afternoon, 2)],
  ];
  for (const [id, description, ids] of suites) {
    writeFileSync(join(root, "evals/suites", `${id}.yaml`), suiteYaml(id, description, ids));
    writeFileSync(join(root, "evals/configs", `${id}.yaml`), configYaml(id, `${description} Ax on Gemini 3.8 Flash.`, id));
  }
  console.log(`shapes ${SHAPES.length}, bank ${bank.length}, morning ${morning.length}, afternoon ${afternoon.length}`);
}

main();
