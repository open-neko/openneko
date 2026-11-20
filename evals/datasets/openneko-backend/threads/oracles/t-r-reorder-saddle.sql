SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select v.name from purchasing.productvendor pv join purchasing.vendor v using (businessentityid)
             join production.product p using (productid) where p.name ilike 'HL Mountain Seat%' order by v.name limit 1),
    (select v.name from purchasing.productvendor pv join purchasing.vendor v using (businessentityid)
             join production.product p using (productid) where p.name ilike 'HL Mountain Seat%' order by v.name desc limit 1)]::text[], null)))
  )::text AS turn_expectations;
