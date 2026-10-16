-- Generated from template vendor-rank. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 0 AS end_date
  FROM purchasing.purchaseorderheader
),
w AS (
  SELECT end_date,
         end_date - 364 AS cur_start,
         end_date - 365 AS prev_end,
         end_date - 729 AS prev_start
  FROM bounds
),
ranked AS (
  SELECT vendor.name AS name,
         sum(header.subtotal + header.taxamt + header.freight) AS value,
         row_number() OVER (ORDER BY sum(header.subtotal + header.taxamt + header.freight) DESC, vendor.name) AS position
  FROM purchasing.purchaseorderheader AS header
  JOIN purchasing.vendor AS vendor ON vendor.businessentityid = header.vendorid
  CROSS JOIN w
  WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date
  GROUP BY vendor.name
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  round(ranked.value, 2)::text AS expected_value,
  round((
    SELECT sum(header.subtotal + header.taxamt + header.freight)
    FROM purchasing.purchaseorderheader AS header
  JOIN purchasing.vendor AS vendor ON vendor.businessentityid = header.vendorid
    WHERE vendor.name = ranked.name
      AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end
  ), 2)::text AS baseline_value,
  ranked.name AS expected_dimension
FROM w
JOIN ranked ON ranked.position = 2;
