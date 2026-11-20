SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.birthdate, pp.lastname limit 1),
    (select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)]::text[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)]::text[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)]::text[], null))),
'null'::jsonb
  )::text AS turn_expectations;
