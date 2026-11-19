SELECT
  '2024-10-31'::text AS anchor_date,
  '2024-10-01'::text AS start_date,
  coalesce(count(*), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.onlineorderflag AND header.orderdate::date BETWEEN '2024-10-01' AND '2024-10-31';
