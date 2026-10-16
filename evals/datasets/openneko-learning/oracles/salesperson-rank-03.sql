-- Generated from template salesperson-rank. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 0 AS end_date
  FROM sales.salesorderheader
),
w AS (
  SELECT end_date,
         end_date - 364 AS cur_start,
         end_date - 365 AS prev_end,
         end_date - 729 AS prev_start
  FROM bounds
),
ranked AS (
  SELECT (person.firstname || ' ' || person.lastname) AS name,
         sum(header.subtotal) AS value,
         row_number() OVER (ORDER BY sum(header.subtotal) DESC, (person.firstname || ' ' || person.lastname)) AS position
  FROM sales.salesorderheader AS header
  JOIN person.person AS person ON person.businessentityid = header.salespersonid
  CROSS JOIN w
  WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date
  GROUP BY (person.firstname || ' ' || person.lastname)
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  round(ranked.value, 2)::text AS expected_value,
  round((
    SELECT sum(header.subtotal)
    FROM sales.salesorderheader AS header
  JOIN person.person AS person ON person.businessentityid = header.salespersonid
    WHERE (person.firstname || ' ' || person.lastname) = ranked.name
      AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end
  ), 2)::text AS baseline_value,
  ranked.name AS expected_dimension
FROM w
JOIN ranked ON ranked.position = 2;
