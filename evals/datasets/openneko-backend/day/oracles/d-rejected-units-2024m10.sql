SELECT
  '2024-10-31'::text AS anchor_date,
  '2024-10-01'::text AS start_date,
  coalesce(round(sum(line.rejectedqty)), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM purchasing.purchaseorderheader AS po JOIN purchasing.purchaseorderdetail AS line USING (purchaseorderid)
WHERE po.orderdate::date BETWEEN '2024-10-01' AND '2024-10-31';
