-- Frozen, synthetic Daily Lead-style dataset for held-out M6 evaluation.
-- Do not edit after a real-provider run without starting a new dataset version.
CREATE TABLE "references" (id integer PRIMARY KEY, label text NOT NULL);
INSERT INTO "references" VALUES (42, 'REF-42');

CREATE TABLE lead_web (
  id integer PRIMARY KEY,
  email text NOT NULL,
  full_name text NOT NULL,
  submitted_at timestamptz NOT NULL
);
CREATE TABLE lead_event (
  id integer PRIMARY KEY,
  email text NOT NULL,
  full_name text NOT NULL,
  received_at timestamptz NOT NULL
);
CREATE TABLE lead_crm (
  id integer PRIMARY KEY,
  email text NOT NULL,
  full_name text NOT NULL,
  created_at timestamptz NOT NULL
);

INSERT INTO lead_web (id, email, full_name, submitted_at)
SELECT n, 'lead' || lpad(n::text, 4, '0') || '@example.test',
  'Web Lead ' || lpad(n::text, 4, '0'),
  timestamptz '2026-09-15 08:00:00+00' + n * interval '1 second'
FROM generate_series(1, 1000) AS n;

INSERT INTO lead_event (id, email, full_name, received_at)
SELECT n, upper('lead' || lpad(n::text, 4, '0') || '@example.test'),
  'Event Lead ' || lpad(n::text, 4, '0'),
  timestamptz '2026-09-15 09:00:00+00' + n * interval '1 second'
FROM generate_series(1, 200) AS n;

INSERT INTO lead_crm (id, email, full_name, created_at)
SELECT n, ' lead' || lpad(n::text, 4, '0') || '@example.test ',
  'CRM Lead ' || lpad(n::text, 4, '0'),
  timestamptz '2026-09-15 07:00:00+00' + n * interval '1 second'
FROM generate_series(1, 50) AS n;

INSERT INTO lead_event (id, email, full_name, received_at)
SELECT n, 'lead' || lpad(n::text, 4, '0') || '@example.test',
  'Event Lead ' || lpad(n::text, 4, '0'),
  timestamptz '2026-09-15 12:00:00+00' + (n - 1000) * interval '1 second'
FROM generate_series(1001, 1010) AS n;

INSERT INTO lead_crm (id, email, full_name, created_at)
SELECT n, 'lead' || lpad(n::text, 4, '0') || '@example.test',
  'CRM Lead ' || lpad(n::text, 4, '0'),
  timestamptz '2026-09-15 13:00:00+00' + (n - 1010) * interval '1 second'
FROM generate_series(1011, 1015) AS n;

-- The lower boundary is inclusive; both adjacent-day records are excluded.
INSERT INTO lead_web VALUES
  (2001, 'previous@example.test', 'Previous Day', timestamptz '2026-09-14 23:59:59+00'),
  (2003, 'boundary@example.test', 'Boundary Lead', timestamptz '2026-09-15 00:00:00+00');
INSERT INTO lead_event VALUES
  (2002, 'next@example.test', 'Next Day', timestamptz '2026-09-16 00:00:00+00');
