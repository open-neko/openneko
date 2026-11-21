SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from sales.salesorderheader h where h.onlineorderflag)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.onlineorderflag and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.onlineorderflag and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.onlineorderflag and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.onlineorderflag and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where not h.onlineorderflag and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where not h.onlineorderflag and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where not h.onlineorderflag and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where not h.onlineorderflag and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null)))
  )::text AS turn_expectations;
