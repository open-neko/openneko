SELECT
  '2025-07-31'::text AS anchor_date,
  '2025-07-01'::text AS start_date,
  coalesce(round(sum(po.subtotal), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM purchasing.purchaseorderheader AS po
WHERE po.orderdate::date BETWEEN '2025-07-01' AND '2025-07-31';
