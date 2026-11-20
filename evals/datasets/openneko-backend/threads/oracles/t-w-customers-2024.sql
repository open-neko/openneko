SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct h.customerid) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct h.customerid) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01' and h.onlineorderflag)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select avg(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select avg(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null)))
  )::text AS turn_expectations;
