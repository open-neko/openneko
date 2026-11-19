SELECT
  '2023-12-31'::text AS anchor_date,
  '2023-10-01'::text AS start_date,
  coalesce(round(sum(header.subtotal), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.orderdate::date BETWEEN '2023-10-01' AND '2023-12-31';
