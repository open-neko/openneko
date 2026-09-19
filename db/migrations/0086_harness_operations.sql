-- Host-owned intent/result records. An absent result never authorizes replay.
CREATE TABLE IF NOT EXISTS harness_operation (
  org_id text NOT NULL,
  run_id text NOT NULL,
  operation_id integer NOT NULL CHECK (operation_id BETWEEN 1 AND 4),
  request jsonb NOT NULL CHECK (octet_length(request::text) <= 65536),
  result jsonb CHECK (result IS NULL OR octet_length(result::text) <= 262144),
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  PRIMARY KEY (org_id, run_id, operation_id),
  FOREIGN KEY (org_id, run_id) REFERENCES harness_run_journal(org_id, run_id) ON DELETE CASCADE,
  CHECK ((result IS NULL) = (finished_at IS NULL))
);
