SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from purchasing.vendor where activeflag)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from purchasing.vendor where activeflag and preferredvendorstatus),
    (select count(*) from purchasing.vendor where preferredvendorstatus)]::numeric[], null)))
  )::text AS turn_expectations;
