SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(l.rejectedqty) from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid join purchasing.purchaseorderdetail l using (purchaseorderid) where h.orderdate >= '2025-01-01' and h.orderdate < '2026-01-01')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select v.name from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid join purchasing.purchaseorderdetail l using (purchaseorderid) where h.orderdate >= '2025-01-01' and h.orderdate < '2026-01-01' group by v.name order by sum(l.rejectedqty) desc, v.name limit 1)]::text[], null))),
'null'::jsonb
  )::text AS turn_expectations;
