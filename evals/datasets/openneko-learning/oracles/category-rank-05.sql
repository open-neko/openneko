-- Generated from template category-rank. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 0 AS end_date
  FROM sales.salesorderheader
),
w AS (
  SELECT end_date,
         end_date - 149 AS cur_start,
         end_date - 150 AS prev_end,
         end_date - 299 AS prev_start
  FROM bounds
),
ranked AS (
  SELECT category.name AS name,
         sum(detail.orderqty * detail.unitprice * (1 - detail.unitpricediscount)) AS value,
         row_number() OVER (ORDER BY sum(detail.orderqty * detail.unitprice * (1 - detail.unitpricediscount)) DESC, category.name) AS position
  FROM sales.salesorderheader AS header
  JOIN sales.salesorderdetail AS detail USING (salesorderid)
  JOIN production.product AS product USING (productid)
  JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
  JOIN production.productcategory AS category ON category.productcategoryid = subcategory.productcategoryid
  CROSS JOIN w
  WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date
  GROUP BY category.name
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  round(ranked.value, 2)::text AS expected_value,
  round((
    SELECT sum(detail.orderqty * detail.unitprice * (1 - detail.unitpricediscount))
    FROM sales.salesorderheader AS header
  JOIN sales.salesorderdetail AS detail USING (salesorderid)
  JOIN production.product AS product USING (productid)
  JOIN production.productsubcategory AS subcategory USING (productsubcategoryid)
  JOIN production.productcategory AS category ON category.productcategoryid = subcategory.productcategoryid
    WHERE category.name = ranked.name
      AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end
  ), 2)::text AS baseline_value,
  ranked.name AS expected_dimension
FROM w
JOIN ranked ON ranked.position = 4;
