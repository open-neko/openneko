SELECT
  '2024-09-30'::text AS anchor_date,
  '2024-09-01'::text AS start_date,
  coalesce(round(sum(detail.unitprice * (1 - detail.unitpricediscount) * detail.orderqty), 2), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
JOIN sales.salesorderdetail AS detail USING (salesorderid)
JOIN production.product AS product USING (productid)
JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
JOIN production.productcategory AS category USING (productcategoryid)
WHERE category.name = 'Accessories' AND header.orderdate::date BETWEEN '2024-09-01' AND '2024-09-30';
