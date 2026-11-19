SELECT
  '2025-03-31'::text AS anchor_date,
  '2025-03-01'::text AS start_date,
  coalesce(sum(work.scrappedqty), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM production.workorder AS work
WHERE work.startdate::date BETWEEN '2025-03-01' AND '2025-03-31';
