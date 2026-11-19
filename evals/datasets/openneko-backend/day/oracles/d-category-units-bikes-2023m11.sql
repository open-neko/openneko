SELECT
  '2023-11-30'::text AS anchor_date,
  '2023-11-01'::text AS start_date,
  coalesce(sum(detail.orderqty), 0)::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension
FROM sales.salesorderheader AS header
JOIN sales.salesorderdetail AS detail USING (salesorderid)
JOIN production.product AS product USING (productid)
JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
JOIN production.productcategory AS category USING (productcategoryid)
WHERE category.name = 'Bikes' AND header.orderdate::date BETWEEN '2023-11-01' AND '2023-11-30';
