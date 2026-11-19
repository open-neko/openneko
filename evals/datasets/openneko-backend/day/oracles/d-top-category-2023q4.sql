WITH ranked AS (
  SELECT category.name AS name, sum(detail.unitprice * (1 - detail.unitpricediscount) * detail.orderqty) AS value
  FROM sales.salesorderheader AS header
JOIN sales.salesorderdetail AS detail USING (salesorderid)
JOIN production.product AS product USING (productid)
JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
JOIN production.productcategory AS category USING (productcategoryid)
  WHERE header.orderdate::date BETWEEN '2023-10-01' AND '2023-12-31'
  GROUP BY category.name
  ORDER BY value DESC, name
  LIMIT 1
)
SELECT
  '2023-12-31'::text AS anchor_date,
  '2023-10-01'::text AS start_date,
  round(value::numeric, 2)::text AS expected_value,
  '0'::text AS baseline_value,
  name::text AS expected_dimension
FROM ranked;
