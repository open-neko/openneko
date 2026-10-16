-- Generated from template freight-share. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 150 AS end_date
  FROM sales.salesorderheader
),
w AS (
  SELECT end_date,
         end_date - 119 AS cur_start,
         end_date - 120 AS prev_end,
         end_date - 239 AS prev_start
  FROM bounds
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  (round(100 * sum(header.freight) FILTER (WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date) / nullif(sum(header.subtotal) FILTER (WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date), 0), 2))::text AS expected_value,
  (round(100 * sum(header.freight) FILTER (WHERE header.orderdate::date BETWEEN w.prev_start AND w.prev_end) / nullif(sum(header.subtotal) FILTER (WHERE header.orderdate::date BETWEEN w.prev_start AND w.prev_end), 0), 2))::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
CROSS JOIN w
GROUP BY w.end_date, w.cur_start, w.prev_start, w.prev_end;
