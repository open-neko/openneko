SELECT
  '2025-06-30'::text AS anchor_date,
  '2025-04-01'::text AS start_date,
  coalesce(round(sum(line.rejectedqty)), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM purchasing.purchaseorderheader AS po JOIN purchasing.purchaseorderdetail AS line USING (purchaseorderid)
WHERE po.orderdate::date BETWEEN '2025-04-01' AND '2025-06-30';
