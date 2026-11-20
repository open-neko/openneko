SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(d.unitprice * (1 - d.unitpricediscount) * d.orderqty) from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Bikes' and h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01')]::numeric[], null)),
  'names', to_jsonb(array_remove(array['Bikes', 'Components', 'Clothing', 'Accessories']::text[], null)))
  )::text AS turn_expectations;
