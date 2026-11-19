SELECT
  '2025-04-30'::text AS anchor_date,
  '2025-04-01'::text AS start_date,
  coalesce(count(DISTINCT header.customerid), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.orderdate::date BETWEEN '2025-04-01' AND '2025-04-30';
