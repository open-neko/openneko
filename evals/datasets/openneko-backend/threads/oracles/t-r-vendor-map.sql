SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
'null'::jsonb,
'null'::jsonb,
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct v.businessentityid) from purchasing.vendor v join person.businessentityaddress ba using (businessentityid)
             join person.address a using (addressid) join person.stateprovince sp using (stateprovinceid) where sp.name = 'Washington')]::numeric[], null)))
  )::text AS turn_expectations;
