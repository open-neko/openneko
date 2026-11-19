SELECT
  '2024-06-30'::text AS anchor_date,
  '2024-04-01'::text AS start_date,
  coalesce(round(sum(po.subtotal), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM purchasing.purchaseorderheader AS po
WHERE po.orderdate::date BETWEEN '2024-04-01' AND '2024-06-30';
