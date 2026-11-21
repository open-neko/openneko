SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.startdate >= '2024-01-01' and w.startdate < '2025-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.enddate >= '2024-01-01' and w.enddate < '2025-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.duedate >= '2024-01-01' and w.duedate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select r.name from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.startdate >= '2024-01-01' and w.startdate < '2025-01-01' and r.name is not null group by r.name order by sum(w.scrappedqty) desc, r.name limit 1),
    (select r.name from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.enddate >= '2024-01-01' and w.enddate < '2025-01-01' and r.name is not null group by r.name order by sum(w.scrappedqty) desc, r.name limit 1)]::text[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select p.name from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.startdate >= '2024-01-01' and w.startdate < '2025-01-01' group by p.name order by sum(w.scrappedqty) desc, p.name limit 1),
    (select p.name from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.enddate >= '2024-01-01' and w.enddate < '2025-01-01' group by p.name order by sum(w.scrappedqty) desc, p.name limit 1)]::text[], null)))
  )::text AS turn_expectations;
