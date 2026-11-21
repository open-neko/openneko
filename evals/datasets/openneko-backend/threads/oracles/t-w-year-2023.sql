SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sub from (select date_trunc('quarter', h.orderdate) q, sum(h.subtotal) sub, sum(h.totaldue) due
     from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01' group by 1 order by 2 desc limit 1) b),
    (select due from (select date_trunc('quarter', h.orderdate) q, sum(h.subtotal) sub, sum(h.totaldue) due
     from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01' group by 1 order by 2 desc limit 1) b)]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pc.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where date_trunc('quarter', h.orderdate) = (select q from (select date_trunc('quarter', h.orderdate) q, sum(h.subtotal) sub, sum(h.totaldue) due
     from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01' group by 1 order by 2 desc limit 1) b) and pc.name is not null
               group by pc.name order by sum(d.unitprice * (1 - d.unitpricediscount) * d.orderqty) desc limit 1)]::text[], null)))
  )::text AS turn_expectations;
