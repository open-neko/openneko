WITH ranked AS (
  SELECT territory.name AS name, sum(header.subtotal) AS value
  FROM sales.salesorderheader AS header JOIN sales.salesterritory AS territory USING (territoryid)
  WHERE header.orderdate::date BETWEEN '2023-07-01' AND '2023-07-31'
  GROUP BY territory.name
  ORDER BY value DESC, name
  LIMIT 1
)
SELECT
  '2023-07-31'::text AS anchor_date,
  '2023-07-01'::text AS start_date,
  round(value::numeric, 2)::text AS expected_value,
  '0'::text AS baseline_value,
  name::text AS expected_dimension
FROM ranked;
