SELECT
  '2022-09-30'::text AS anchor_date,
  '2022-07-01'::text AS start_date,
  coalesce(count(DISTINCT header.customerid), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.orderdate::date BETWEEN '2022-07-01' AND '2022-09-30';
