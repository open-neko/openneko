-- Generated from template territory-rank. Do not edit; run pnpm --filter @neko/evals learning:generate.
WITH bounds AS (
  SELECT max(orderdate)::date - 0 AS end_date
  FROM sales.salesorderheader
),
w AS (
  SELECT end_date,
         end_date - 269 AS cur_start,
         end_date - 270 AS prev_end,
         end_date - 539 AS prev_start
  FROM bounds
),
ranked AS (
  SELECT territory.name AS name,
         sum(header.totaldue) AS value,
         row_number() OVER (ORDER BY sum(header.totaldue) DESC, territory.name) AS position
  FROM sales.salesorderheader AS header
  JOIN sales.salesterritory AS territory USING (territoryid)
  CROSS JOIN w
  WHERE header.orderdate::date BETWEEN w.cur_start AND w.end_date
  GROUP BY territory.name
)
SELECT
  w.end_date::text AS anchor_date,
  w.cur_start::text AS start_date,
  round(ranked.value, 2)::text AS expected_value,
  round((
    SELECT sum(header.totaldue)
    FROM sales.salesorderheader AS header
  JOIN sales.salesterritory AS territory USING (territoryid)
    WHERE territory.name = ranked.name
      AND header.orderdate::date BETWEEN w.prev_start AND w.prev_end
  ), 2)::text AS baseline_value,
  ranked.name AS expected_dimension
FROM w
JOIN ranked ON ranked.position = 3;
