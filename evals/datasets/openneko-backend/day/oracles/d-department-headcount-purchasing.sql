SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  coalesce(count(*), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM humanresources.employeedepartmenthistory AS history JOIN humanresources.department AS department USING (departmentid)
WHERE department.name = 'Purchasing' AND history.enddate IS NULL;
