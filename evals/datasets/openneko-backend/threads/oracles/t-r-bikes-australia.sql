SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('any_names', to_jsonb(array_remove(array[split_part((select p.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Bikes' and h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') group by p.name order by sum(d.orderqty) desc, p.name limit 1), ' ', 1),
    split_part((select p.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Bikes' and h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') group by p.name order by sum(d.unitprice * (1 - d.unitpricediscount) * d.orderqty) desc, p.name limit 1), ' ', 1)]::text[], null))),
'null'::jsonb,
'null'::jsonb,
'null'::jsonb
  )::text AS turn_expectations;
