-- Optional Harness proposal identity and frozen approval input. Hermes rows stay null.
ALTER TABLE action_request ADD COLUMN IF NOT EXISTS harness_operation_id integer;
ALTER TABLE action_request ADD COLUMN IF NOT EXISTS harness_proposal jsonb;
ALTER TABLE action_request ADD COLUMN IF NOT EXISTS harness_prepared jsonb;
CREATE UNIQUE INDEX IF NOT EXISTS action_request_harness_operation_unique
  ON action_request(org_id,work_run_id,harness_operation_id) WHERE harness_operation_id IS NOT NULL;
ALTER TABLE action_request ADD CONSTRAINT action_request_harness_operation_valid CHECK (
  harness_operation_id IS NULL OR (harness_operation_id BETWEEN 1 AND 4 AND work_run_id IS NOT NULL
    AND actor_backend IS NOT DISTINCT FROM 'harness' AND harness_proposal IS NOT NULL));
ALTER TABLE action_request ADD CONSTRAINT action_request_harness_proposal_bound CHECK (
  harness_proposal IS NULL OR octet_length(harness_proposal::text) <= 65536);
ALTER TABLE action_request ADD CONSTRAINT action_request_harness_prepared_bound CHECK (
  harness_prepared IS NULL OR octet_length(harness_prepared::text) <= 65536);
