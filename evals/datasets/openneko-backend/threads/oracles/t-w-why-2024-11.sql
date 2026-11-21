SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2024-11-01' and h.orderdate < '2024-11-01'::date + interval '1 month'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2024-11-01' and h.orderdate < '2024-11-01'::date + interval '1 month')]::numeric[], null))),
'null'::jsonb
  )::text AS turn_expectations;
