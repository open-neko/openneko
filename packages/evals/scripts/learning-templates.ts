export type TemplateParams = Readonly<Record<string, string | number>>;

export type SkillDefinition = { name: string; description: string; body: string };

export type LearningTemplate = {
  id: string;
  split: "siblings" | "holdout";
  difficulty: "grouped-rank" | "join" | "time-window";
  capabilityTags: readonly string[];
  semantics: readonly string[];
  winner: boolean;
  skill: SkillDefinition;
  /** Skills with similar names or descriptions whose method gives a wrong answer. */
  decoys: readonly SkillDefinition[];
  question(params: TemplateParams): string;
  sql(params: TemplateParams): string;
  params: readonly TemplateParams[];
};

const ANSWER_CONTRACT =
  "Put each requested field on its own line using these exact labels: Current value, Comparison value, Current window, and Winner; omit Winner when no named winner was requested.";

const ORDINALS = ["", "first", "second", "third", "fourth", "fifth"];

function ordinal(rank: number): string {
  return ORDINALS[rank] ?? `${rank}th`;
}

function windowCte(anchorTable: string, days: number, offset: number): string {
  return `WITH bounds AS (
  SELECT max(orderdate)::date - ${offset} AS end_date
  FROM ${anchorTable}
),
w AS (
  SELECT end_date,
         end_date - ${days - 1} AS cur_start,
         end_date - ${days} AS prev_end,
         end_date - ${2 * days - 1} AS prev_start
  FROM bounds
)`;
}

function rankSql(input: {
  anchorTable: string;
  from: string;
  dateColumn: string;
  name: string;
  value: string;
  days: number;
  rank: number;
}): string {
  return `${windowCte(input.anchorTable, input.days, 0)},
ranked AS (
  SELECT ${input.name} AS name,
         sum(${input.value}) AS value,
         row_number() OVER (ORDER BY sum(${input.value}) DESC, ${input.name}) AS position
  FROM ${input.from}
  CROSS JOIN w
  WHERE ${input.dateColumn}::date BETWEEN w.cur_start AND w.end_date
  GROUP BY ${input.name}
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  round(ranked.value, 2)::text AS expected_value,
  round((
    SELECT sum(${input.value})
    FROM ${input.from}
    WHERE ${input.name} = ranked.name
      AND ${input.dateColumn}::date BETWEEN w.prev_start AND w.prev_end
  ), 2)::text AS baseline_value,
  ranked.name AS expected_dimension
FROM w
JOIN ranked ON ranked.position = ${input.rank};
`;
}

function scalarSql(input: {
  anchorTable: string;
  from: string;
  days: number;
  offset: number;
  measure: (start: string, end: string) => string;
}): string {
  return `${windowCte(input.anchorTable, input.days, input.offset)}
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  (${input.measure("w.cur_start", "w.end_date")})::text AS expected_value,
  (${input.measure("w.prev_start", "w.prev_end")})::text AS baseline_value,
  NULL::text AS expected_dimension
FROM ${input.from}
CROSS JOIN w
GROUP BY w.end_date, w.cur_start, w.prev_start, w.prev_end;
`;
}

function windowPhrase(days: number, offset: number, source: string): string {
  return offset === 0
    ? `the ${days}-day window that ends on the latest ${source} date`
    : `the ${days}-day window that ends ${offset} days before the latest ${source} date`;
}

const SALES_LINES = `sales.salesorderheader AS header
  JOIN sales.salesorderdetail AS detail USING (salesorderid)`;

export const TEMPLATES: readonly LearningTemplate[] = [
  {
    id: "territory-rank",
    split: "siblings",
    difficulty: "grouped-rank",
    capabilityTags: ["aggregate.rank", "aggregate.sum", "date.anchor", "date.window", "relation.join", "sales.orders", "sales.territories", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-RANK", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: true,
    skill: {
      name: "aw-territory-rank",
      description: "Rank AdventureWorks sales territories by order value in a day-count window and compare the chosen territory with the window before.",
      body: `Use this skill when someone asks which sales territory ranks at a given position by order value.

1. Find the latest sales-order date in the connected AdventureWorks source. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total the order value per territory in the current window, then sort from highest to lowest.
4. Pick the territory at the requested position.
5. Total the same territory in the window of equal length just before the current window.
6. Report the territory, both totals and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-territory-growth",
        description: "Rank AdventureWorks sales territories by revenue growth between two calendar years.",
        body: `Use this skill when someone asks which territory grew fastest.

1. Pick the two most recent complete calendar years.
2. Sum the order subtotal per territory for each year.
3. Divide the later year by the earlier year and subtract one.
4. Rank territories by that growth rate and report the rate as a percentage.`,
      },
      {
        name: "aw-region-rank",
        description: "Rank AdventureWorks territory groups such as North America, Europe and Pacific by order subtotal for the current calendar year.",
        body: `Use this skill when someone asks which region leads in sales.

1. Take orders dated from 1 January of the latest calendar year to today.
2. Sum the order subtotal per territory group, not per territory.
3. Rank the groups from highest to lowest and report the group name and subtotal.`,
      },
    ],
    question: (p) =>
      `Which sales territory ranked ${ordinal(Number(p.rank))} by total order value, including tax and freight, over ${windowPhrase(Number(p.days), 0, "sales-order")}? Every day-count window includes both endpoints. Give that territory's value and compare it with the same territory in the immediately preceding ${p.days}-day window. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      rankSql({
        anchorTable: "sales.salesorderheader",
        from: "sales.salesorderheader AS header\n  JOIN sales.salesterritory AS territory USING (territoryid)",
        dateColumn: "header.orderdate",
        name: "territory.name",
        value: "header.totaldue",
        days: Number(p.days),
        rank: Number(p.rank),
      }),
    params: [
      { days: 90, rank: 1 },
      { days: 90, rank: 2 },
      { days: 120, rank: 3 },
      { days: 150, rank: 1 },
      { days: 180, rank: 2 },
      { days: 210, rank: 4 },
      { days: 240, rank: 1 },
      { days: 270, rank: 3 },
      { days: 300, rank: 2 },
      { days: 365, rank: 4 },
    ],
  },
  {
    id: "category-rank",
    split: "siblings",
    difficulty: "grouped-rank",
    capabilityTags: ["aggregate.rank", "aggregate.sum", "date.anchor", "date.window", "relation.join", "production.categories", "production.products", "sales.order-lines", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-RANK", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: true,
    skill: {
      name: "aw-category-rank",
      description: "Rank AdventureWorks product categories by sales-line revenue in a day-count window and compare the chosen category with the window before.",
      body: `Use this skill when someone asks which product category ranks at a given position by line revenue.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Connect each sales-order line to its product, the product subcategory and the category.
4. Sum line revenue per category (quantity times unit price, less the discount) for orders dated in the window, then sort from highest to lowest.
5. Pick the category at the requested position and total it again for the window of equal length just before.
6. Report the category, both totals and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-subcategory-rank",
        description: "Rank AdventureWorks product subcategories by units sold in a day-count window.",
        body: `Use this skill when someone asks which product line sells the most units.

1. Find the latest sales-order date and build the window from the day count.
2. Connect sales-order lines to product subcategories.
3. Sum the ordered quantity per subcategory and rank from highest to lowest.
4. Report the subcategory name and the unit count.`,
      },
      {
        name: "aw-category-margin",
        description: "Rank AdventureWorks product categories by gross margin from list price and standard cost.",
        body: `Use this skill when someone asks which category is most profitable.

1. For each product, subtract standard cost from list price.
2. Average that margin per category across the product catalog.
3. Rank categories by average margin and report the margin per unit.`,
      },
    ],
    question: (p) =>
      `Which product category ranked ${ordinal(Number(p.rank))} by sales-line revenue (order quantity times unit price, less the line discount) over ${windowPhrase(Number(p.days), 0, "sales-order")}? Every day-count window includes both endpoints. Give that category's revenue and compare it with the same category in the immediately preceding ${p.days}-day window. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      rankSql({
        anchorTable: "sales.salesorderheader",
        from: `${SALES_LINES}
  JOIN production.product AS product USING (productid)
  JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
  JOIN production.productcategory AS category ON category.productcategoryid = subcategory.productcategoryid`,
        dateColumn: "header.orderdate",
        name: "category.name",
        value: "detail.orderqty * detail.unitprice * (1 - detail.unitpricediscount)",
        days: Number(p.days),
        rank: Number(p.rank),
      }),
    params: [
      { days: 60, rank: 2 },
      { days: 90, rank: 1 },
      { days: 90, rank: 3 },
      { days: 120, rank: 2 },
      { days: 150, rank: 4 },
      { days: 180, rank: 3 },
      { days: 200, rank: 1 },
      { days: 240, rank: 4 },
      { days: 300, rank: 3 },
      { days: 365, rank: 2 },
    ],
  },
  {
    id: "color-units",
    split: "siblings",
    difficulty: "join",
    capabilityTags: ["aggregate.sum", "date.anchor", "date.window", "filter.boolean", "relation.join", "production.products", "sales.order-lines", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: false,
    skill: {
      name: "aw-color-units",
      description: "Count AdventureWorks units sold for products of one color in a day-count window and compare with the window before.",
      body: `Use this skill when someone asks how many units of products in one color were sold.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Connect sales-order lines to their products and keep only products of the requested color.
4. Sum the ordered quantity on lines whose order date falls in the window.
5. Repeat for the window of equal length just before the current window.
6. Report both unit totals and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-color-revenue",
        description: "Report AdventureWorks sales revenue for products of one color by calendar month.",
        body: `Use this skill when someone asks how a product color performs.

1. Group sales-order lines by calendar month of the order date.
2. Keep products of the requested color.
3. Sum the line revenue per month and report the latest complete month against the month before.`,
      },
      {
        name: "aw-color-inventory",
        description: "Count AdventureWorks units on hand for products of one color.",
        body: `Use this skill when someone asks how many units of a color we have.

1. Connect product inventory rows to products of the requested color.
2. Sum the quantity on hand across all locations.
3. Report the total units on hand.`,
      },
    ],
    question: (p) =>
      `How many units of ${p.color} products did we sell over ${windowPhrase(Number(p.days), 0, "sales-order")}, compared with the immediately preceding ${p.days}-day window? Every day-count window includes both endpoints. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      scalarSql({
        anchorTable: "sales.salesorderheader",
        from: `${SALES_LINES}\n  JOIN production.product AS product USING (productid)`,
        days: Number(p.days),
        offset: 0,
        measure: (start, end) =>
          `coalesce(sum(detail.orderqty) FILTER (WHERE product.color = '${p.color}' AND header.orderdate::date BETWEEN ${start} AND ${end}), 0)`,
      }),
    params: [
      { color: "Black", days: 60 },
      { color: "Red", days: 90 },
      { color: "Silver", days: 120 },
      { color: "Yellow", days: 150 },
      { color: "Blue", days: 180 },
      { color: "Black", days: 210 },
      { color: "Red", days: 240 },
      { color: "Silver", days: 270 },
      { color: "Yellow", days: 300 },
      { color: "Blue", days: 365 },
    ],
  },
  {
    id: "online-share",
    split: "siblings",
    difficulty: "time-window",
    capabilityTags: ["aggregate.ratio", "aggregate.count", "date.anchor", "date.window", "filter.boolean", "sales.orders", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: false,
    skill: {
      name: "aw-online-share",
      description: "Report the share of AdventureWorks sales orders placed online in a day-count window that can end before the latest order date.",
      body: `Use this skill when someone asks what share of sales orders were placed online in a window.

1. Find the latest sales-order date. That date is the anchor.
2. Move the window end back from the anchor by the requested number of days. With no offset, the window ends on the anchor.
3. Build the current window from the day count. Both endpoints count as days in the window.
4. Count all orders in the window and the orders flagged as online.
5. Divide online orders by all orders and express the result as a percentage.
6. Repeat for the window of equal length just before the current window, then report both percentages and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-online-revenue-share",
        description: "Report the share of AdventureWorks revenue that came from online orders in a calendar quarter.",
        body: `Use this skill when someone asks how much of our revenue is online.

1. Take the latest complete calendar quarter.
2. Sum order subtotal for online orders and for all orders.
3. Divide online subtotal by total subtotal and report it as a percentage against the previous quarter.`,
      },
      {
        name: "aw-reseller-share",
        description: "Report the share of AdventureWorks orders placed by resellers in the last full calendar month.",
        body: `Use this skill when someone asks how much business comes through stores.

1. Take the last full calendar month before the latest order date.
2. Count orders with a salesperson and all orders.
3. Divide and report the reseller share as a percentage.`,
      },
    ],
    question: (p) =>
      `What percentage of our sales orders were placed online over ${windowPhrase(Number(p.days), Number(p.offset), "sales-order")}, compared with the immediately preceding ${p.days}-day window? Every day-count window includes both endpoints. Report percentages with two decimals. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      scalarSql({
        anchorTable: "sales.salesorderheader",
        from: "sales.salesorderheader AS header",
        days: Number(p.days),
        offset: Number(p.offset),
        measure: (start, end) =>
          `round(100.0 * count(*) FILTER (WHERE header.onlineorderflag AND header.orderdate::date BETWEEN ${start} AND ${end}) / nullif(count(*) FILTER (WHERE header.orderdate::date BETWEEN ${start} AND ${end}), 0), 2)`,
      }),
    params: [
      { days: 30, offset: 90 },
      { days: 30, offset: 60 },
      { days: 45, offset: 150 },
      { days: 60, offset: 90 },
      { days: 60, offset: 30 },
      { days: 90, offset: 120 },
      { days: 90, offset: 45 },
      { days: 120, offset: 0 },
      { days: 120, offset: 200 },
      { days: 180, offset: 100 },
    ],
  },
  {
    id: "discount-depth",
    split: "siblings",
    difficulty: "join",
    capabilityTags: ["aggregate.average", "date.anchor", "date.window", "filter.numeric", "relation.join", "sales.order-lines", "sales.orders", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-SCALAR", "CALC-FILTER", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: false,
    skill: {
      name: "aw-discount-depth",
      description: "Report the average discount on discounted AdventureWorks sales-order lines above a quantity floor in a day-count window.",
      body: `Use this skill when someone asks how deep our line-item discounts were.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Take sales-order lines whose order date falls in the window.
4. Keep lines that received a discount and whose quantity meets the requested floor.
5. Average the discount on those lines and express it as a percentage.
6. Repeat for the window of equal length just before the current window, then report both averages and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-discount-revenue",
        description: "Report the total discount value given on AdventureWorks sales lines in a calendar year.",
        body: `Use this skill when someone asks how much we gave away in discounts.

1. Take order lines in the latest calendar year.
2. Multiply quantity, unit price and the line discount for each line.
3. Sum the discount value and report it in currency against the year before.`,
      },
      {
        name: "aw-special-offer",
        description: "List AdventureWorks special offers and their published discount percentages.",
        body: `Use this skill when someone asks about our discount levels.

1. Read the special offer table.
2. Keep offers whose dates cover the latest order date.
3. Report the average published discount percentage across those offers.`,
      },
    ],
    question: (p) =>
      `On sales-order lines that received a price discount and had an order quantity of at least ${p.quantity}, what was the average discount percentage over ${windowPhrase(Number(p.days), 0, "sales-order")}, compared with the immediately preceding ${p.days}-day window? Every day-count window includes both endpoints. Report percentages with two decimals. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      scalarSql({
        anchorTable: "sales.salesorderheader",
        from: SALES_LINES,
        days: Number(p.days),
        offset: 0,
        measure: (start, end) =>
          `round(100 * avg(detail.unitpricediscount) FILTER (WHERE detail.unitpricediscount > 0 AND detail.orderqty >= ${p.quantity} AND header.orderdate::date BETWEEN ${start} AND ${end}), 2)`,
      }),
    params: [
      { days: 180, quantity: 1 },
      { days: 180, quantity: 4 },
      { days: 200, quantity: 2 },
      { days: 240, quantity: 3 },
      { days: 240, quantity: 6 },
      { days: 270, quantity: 1 },
      { days: 300, quantity: 5 },
      { days: 330, quantity: 2 },
      { days: 365, quantity: 3 },
      { days: 365, quantity: 8 },
    ],
  },
  {
    id: "vendor-rank",
    split: "siblings",
    difficulty: "grouped-rank",
    capabilityTags: ["aggregate.rank", "aggregate.sum", "date.anchor", "date.window", "relation.join", "purchasing.orders", "purchasing.vendors", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-RANK", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: true,
    skill: {
      name: "aw-vendor-rank",
      description: "Rank AdventureWorks vendors by purchase-order value in a day-count window and compare the chosen vendor with the window before.",
      body: `Use this skill when someone asks which vendor ranks at a given position by purchase spend.

1. Find the latest purchase-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total each vendor's purchase-order value in the window, then sort from highest to lowest.
4. Pick the vendor at the requested position.
5. Total the same vendor in the window of equal length just before the current window.
6. Report the vendor name, both totals and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-vendor-lead-time",
        description: "Rank AdventureWorks vendors by average lead time from product vendor terms.",
        body: `Use this skill when someone asks which vendors are slowest or fastest.

1. Read the product vendor terms for each vendor.
2. Average the lead time in days per vendor.
3. Rank vendors by average lead time and report the days.`,
      },
      {
        name: "aw-vendor-receipts",
        description: "Rank AdventureWorks vendors by quantity received on purchase-order lines in a calendar year.",
        body: `Use this skill when someone asks which vendors supply the most.

1. Take purchase-order lines dated in the latest calendar year.
2. Sum the received quantity per vendor.
3. Rank vendors by received quantity and report the units.`,
      },
    ],
    question: (p) =>
      `Which vendor ranked ${ordinal(Number(p.rank))} by total purchase-order value, including tax and freight, over ${windowPhrase(Number(p.days), 0, "purchase-order")}? Every day-count window includes both endpoints. Give that vendor's value and compare it with the same vendor in the immediately preceding ${p.days}-day window. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      rankSql({
        anchorTable: "purchasing.purchaseorderheader",
        from: "purchasing.purchaseorderheader AS header\n  JOIN purchasing.vendor AS vendor ON vendor.businessentityid = header.vendorid",
        dateColumn: "header.orderdate",
        name: "vendor.name",
        value: "header.subtotal + header.taxamt + header.freight",
        days: Number(p.days),
        rank: Number(p.rank),
      }),
    params: [
      { days: 90, rank: 1 },
      { days: 90, rank: 3 },
      { days: 120, rank: 2 },
      { days: 150, rank: 1 },
      { days: 180, rank: 4 },
      { days: 210, rank: 2 },
      { days: 240, rank: 3 },
      { days: 270, rank: 1 },
      { days: 300, rank: 5 },
      { days: 365, rank: 2 },
    ],
  },
  {
    id: "salesperson-rank",
    split: "holdout",
    difficulty: "grouped-rank",
    capabilityTags: ["aggregate.rank", "aggregate.sum", "date.anchor", "date.window", "relation.join", "sales.orders", "sales.salespeople", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-RANK", "CALC-TIME", "CALC-JOIN", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: true,
    skill: {
      name: "aw-salesperson-rank",
      description: "Rank AdventureWorks salespeople by order subtotal in a day-count window and compare the chosen person with the window before.",
      body: `Use this skill when someone asks which salesperson ranks at a given position by sales.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total the order subtotal per salesperson in the window, then sort from highest to lowest.
4. Pick the salesperson at the requested position and look up the full name.
5. Total the same salesperson in the window of equal length just before the current window.
6. Report the full name, both totals and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-salesperson-quota",
        description: "Rank AdventureWorks salespeople by year-to-date sales recorded on the salesperson record.",
        body: `Use this skill when someone asks who the top seller is.

1. Read the year-to-date sales column on each salesperson record.
2. Rank salespeople from highest to lowest.
3. Report the name and the year-to-date amount.`,
      },
      {
        name: "aw-territory-manager",
        description: "Find the AdventureWorks salesperson assigned to a sales territory.",
        body: `Use this skill when someone asks who covers a territory.

1. Read the salesperson records with their territory.
2. Match the requested territory.
3. Report the salesperson name.`,
      },
    ],
    question: (p) =>
      `Which salesperson ranked ${ordinal(Number(p.rank))} by order subtotal, before tax and freight, over ${windowPhrase(Number(p.days), 0, "sales-order")}? Every day-count window includes both endpoints. Give the full name, that person's subtotal, and compare it with the same person in the immediately preceding ${p.days}-day window. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      rankSql({
        anchorTable: "sales.salesorderheader",
        from: "sales.salesorderheader AS header\n  JOIN person.person AS person ON person.businessentityid = header.salespersonid",
        dateColumn: "header.orderdate",
        name: "(person.firstname || ' ' || person.lastname)",
        value: "header.subtotal",
        days: Number(p.days),
        rank: Number(p.rank),
      }),
    params: [
      { days: 120, rank: 1 },
      { days: 180, rank: 3 },
      { days: 365, rank: 2 },
    ],
  },
  {
    id: "freight-share",
    split: "holdout",
    difficulty: "time-window",
    capabilityTags: ["aggregate.ratio", "aggregate.sum", "date.anchor", "date.window", "sales.orders", "work.graphjin-direct", "work.skill-load"],
    semantics: ["DATA-DIRECT", "CALC-SCALAR", "CALC-TIME", "WORK-SKILLS", "WORK-SKILL-LEARN"],
    winner: false,
    skill: {
      name: "aw-freight-share",
      description: "Report AdventureWorks freight as a percentage of order subtotal in a day-count window that can end before the latest order date.",
      body: `Use this skill when someone asks how large freight is compared with order subtotal.

1. Find the latest sales-order date. That date is the anchor.
2. Move the window end back from the anchor by the requested number of days. With no offset, the window ends on the anchor.
3. Build the current window from the day count. Both endpoints count as days in the window.
4. Sum freight and sum subtotal for orders in the window.
5. Divide freight by subtotal and express the result as a percentage.
6. Repeat for the window of equal length just before the current window, then report both percentages and the current window dates.`,
    },
    decoys: [
      {
        name: "aw-freight-cost",
        description: "Report total AdventureWorks freight cost by ship method in a calendar month.",
        body: `Use this skill when someone asks what shipping costs us.

1. Take orders in the latest complete calendar month.
2. Sum freight per ship method.
3. Report each ship method's freight in currency.`,
      },
      {
        name: "aw-tax-share",
        description: "Report AdventureWorks sales tax as a percentage of order subtotal in a day-count window.",
        body: `Use this skill when someone asks about our tax burden on orders.

1. Find the latest sales-order date and build the window from the day count.
2. Sum tax and subtotal for orders in the window.
3. Divide tax by subtotal and report the percentage against the window before.`,
      },
    ],
    question: (p) =>
      `What was freight as a percentage of order subtotal over ${windowPhrase(Number(p.days), Number(p.offset), "sales-order")}, compared with the immediately preceding ${p.days}-day window? Every day-count window includes both endpoints. Report percentages with two decimals. ${ANSWER_CONTRACT}`,
    sql: (p) =>
      scalarSql({
        anchorTable: "sales.salesorderheader",
        from: "sales.salesorderheader AS header",
        days: Number(p.days),
        offset: Number(p.offset),
        measure: (start, end) =>
          `round(100 * sum(header.freight) FILTER (WHERE header.orderdate::date BETWEEN ${start} AND ${end}) / nullif(sum(header.subtotal) FILTER (WHERE header.orderdate::date BETWEEN ${start} AND ${end}), 0), 2)`,
      }),
    params: [
      { days: 60, offset: 0 },
      { days: 90, offset: 30 },
      { days: 120, offset: 150 },
    ],
  },
];
