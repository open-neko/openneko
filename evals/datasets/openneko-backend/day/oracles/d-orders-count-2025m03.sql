SELECT
  '2025-03-31'::text AS anchor_date,
  '2025-03-01'::text AS start_date,
  coalesce(count(*), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.orderdate::date BETWEEN '2025-03-01' AND '2025-03-31';
