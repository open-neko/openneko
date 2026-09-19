-- Opt-in Harness ownership and receipts. Hermes never reads this table.
CREATE TABLE IF NOT EXISTS harness_run_journal (
  org_id text NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
  run_id text NOT NULL,
  fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
  result jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, run_id),
  CHECK (result IS NULL OR octet_length(result::text) <= 8388608)
);
