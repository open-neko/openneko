SELECT
  '2023-04-30'::text AS anchor_date,
  '2023-04-01'::text AS start_date,
  coalesce(round(sum(header.subtotal), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)
WHERE territory.name = 'Canada' AND header.orderdate::date BETWEEN '2023-04-01' AND '2023-04-30';
