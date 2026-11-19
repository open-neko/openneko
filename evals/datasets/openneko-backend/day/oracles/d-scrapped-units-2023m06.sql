SELECT
  '2023-06-30'::text AS anchor_date,
  '2023-06-01'::text AS start_date,
  coalesce(sum(work.scrappedqty), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM production.workorder AS work
WHERE work.startdate::date BETWEEN '2023-06-01' AND '2023-06-30';
