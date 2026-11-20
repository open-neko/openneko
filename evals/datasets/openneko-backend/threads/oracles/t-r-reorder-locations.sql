SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0))]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select loc.name from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) group by loc.name order by sum(i.quantity), loc.name limit 1)]::text[], null)))
  )::text AS turn_expectations;
