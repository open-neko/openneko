SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
'null'::jsonb,
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(d.orderqty) from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Components' and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select p.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Components' and h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01' group by p.name order by sum(d.orderqty) desc, p.name limit 1)]::text[], null)))
  )::text AS turn_expectations;
