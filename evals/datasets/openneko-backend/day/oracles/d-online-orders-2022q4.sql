SELECT
  '2022-12-31'::text AS anchor_date,
  '2022-10-01'::text AS start_date,
  coalesce(count(*), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
WHERE header.onlineorderflag AND header.orderdate::date BETWEEN '2022-10-01' AND '2022-12-31';
