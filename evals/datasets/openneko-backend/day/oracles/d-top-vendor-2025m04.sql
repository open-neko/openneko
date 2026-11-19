WITH ranked AS (
  SELECT vendor.name AS name, sum(po.subtotal) AS value
  FROM purchasing.purchaseorderheader AS po JOIN purchasing.vendor AS vendor ON vendor.businessentityid = po.vendorid
  WHERE po.orderdate::date BETWEEN '2025-04-01' AND '2025-04-30'
  GROUP BY vendor.name
  ORDER BY value DESC, name
  LIMIT 1
)
SELECT
  '2025-04-30'::text AS anchor_date,
  '2025-04-01'::text AS start_date,
  round(value::numeric, 2)::text AS expected_value,
  '0'::text AS baseline_value,
  name::text AS expected_dimension
FROM ranked;
