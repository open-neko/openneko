SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where true group by s.name order by sum(h.subtotal) desc, s.name limit 1),
    (select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1),
    (select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1)]::text[], null)))
  )::text AS turn_expectations;
