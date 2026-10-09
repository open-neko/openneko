-- Independent verifier query; never mount this file into an agent sandbox.
COPY (
  WITH observed AS (
    SELECT lower(btrim(email)) AS email, full_name, submitted_at AS seen_at, 'web' AS source FROM lead_web
    UNION ALL
    SELECT lower(btrim(email)), full_name, received_at, 'event' FROM lead_event
    UNION ALL
    SELECT lower(btrim(email)), full_name, created_at, 'crm' FROM lead_crm
  ), target_day AS (
    SELECT * FROM observed
    WHERE seen_at >= timestamptz '2026-09-15 00:00:00+00'
      AND seen_at < timestamptz '2026-09-16 00:00:00+00'
  ), ranked AS (
    SELECT *, row_number() OVER (PARTITION BY email ORDER BY seen_at, source) AS rank FROM target_day
  ), grouped AS (
    SELECT email, string_agg(DISTINCT source, '|' ORDER BY source) AS sources,
      count(*) AS occurrences FROM target_day GROUP BY email
  )
  SELECT g.email, r.full_name,
    to_char(r.seen_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS first_seen_utc,
    g.sources, g.occurrences
  FROM grouped g JOIN ranked r ON r.email = g.email AND r.rank = 1
  ORDER BY g.email
) TO STDOUT WITH (FORMAT CSV, HEADER TRUE);
