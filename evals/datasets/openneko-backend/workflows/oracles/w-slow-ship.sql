SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[])),
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[])),
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[])),
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[])),
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[])),
jsonb_build_object('values', to_jsonb(array[(select count(*) from sales.salesorderheader h where h.status = 1 and h.orderdate < '2025-06-29'::date - 5)]::numeric[]))
  )::text AS turn_expectations;
