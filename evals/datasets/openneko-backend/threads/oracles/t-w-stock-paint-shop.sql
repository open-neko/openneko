SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(i.quantity) from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Paint Shop')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select p.name from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Paint Shop' group by p.name order by sum(i.quantity) desc, p.name limit 1)]::text[], null))),
'null'::jsonb
  )::text AS turn_expectations;
