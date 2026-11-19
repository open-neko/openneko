WITH ranked AS (
  SELECT product.name AS name, sum(detail.orderqty) AS value
  FROM sales.salesorderheader AS header
JOIN sales.salesorderdetail AS detail USING (salesorderid)
JOIN production.product AS product USING (productid)
JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
JOIN production.productcategory AS category USING (productcategoryid)
  WHERE category.name = 'Clothing' AND header.orderdate::date BETWEEN '2024-05-01' AND '2024-05-31'
  GROUP BY product.name
  ORDER BY value DESC, name
  LIMIT 1
)
SELECT
  '2024-05-31'::text AS anchor_date,
  '2024-05-01'::text AS start_date,
  round(value::numeric, 2)::text AS expected_value,
  '0'::text AS baseline_value,
  name::text AS expected_dimension
FROM ranked;
