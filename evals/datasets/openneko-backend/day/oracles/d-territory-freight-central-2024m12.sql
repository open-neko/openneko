SELECT
  '2024-12-31'::text AS anchor_date,
  '2024-12-01'::text AS start_date,
  coalesce(round(sum(header.freight), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)
WHERE territory.name = 'Central' AND header.orderdate::date BETWEEN '2024-12-01' AND '2024-12-31';
