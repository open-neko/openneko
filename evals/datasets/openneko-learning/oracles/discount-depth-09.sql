-- Generated from template discount-depth. Do not edit; run pnpm --filter @neko/evals learning:generate.
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
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  (round(100 * avg(detail.unitpricediscount) FILTER (WHERE detail.unitpricediscount > 0 AND detail.orderqty >= 3 AND header.orderdate::date BETWEEN w.cur_start AND w.end_date), 2))::text AS expected_value,
  (round(100 * avg(detail.unitpricediscount) FILTER (WHERE detail.unitpricediscount > 0 AND detail.orderqty >= 3 AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end), 2))::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
  JOIN sales.salesorderdetail AS detail USING (salesorderid)
CROSS JOIN w
GROUP BY w.end_date, w.cur_start, w.prev_start, w.prev_end;
