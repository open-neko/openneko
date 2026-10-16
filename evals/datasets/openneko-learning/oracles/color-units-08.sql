-- Generated from template color-units. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 0 AS end_date
  FROM sales.salesorderheader
),
w AS (
  SELECT end_date,
         end_date - 269 AS cur_start,
         end_date - 270 AS prev_end,
         end_date - 539 AS prev_start
  FROM bounds
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  (coalesce(sum(detail.orderqty) FILTER (WHERE product.color = 'Silver' AND header.orderdate::date BETWEEN w.cur_start AND w.end_date), 0))::text AS expected_value,
  (coalesce(sum(detail.orderqty) FILTER (WHERE product.color = 'Silver' AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end), 0))::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
  JOIN sales.salesorderdetail AS detail USING (salesorderid)
  JOIN production.product AS product USING (productid)
CROSS JOIN w
GROUP BY w.end_date, w.cur_start, w.prev_start, w.prev_end;
