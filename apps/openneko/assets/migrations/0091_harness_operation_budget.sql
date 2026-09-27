-- Widen the storage ceiling; the bound broker token enforces each run's limit.
ALTER TABLE harness_operation DROP CONSTRAINT harness_operation_operation_id_check;
ALTER TABLE harness_operation ADD CONSTRAINT harness_operation_operation_id_check
  CHECK (operation_id BETWEEN 1 AND 32);
ALTER TABLE action_request DROP CONSTRAINT action_request_harness_operation_valid;
ALTER TABLE action_request ADD CONSTRAINT action_request_harness_operation_valid CHECK (
  harness_operation_id IS NULL OR (harness_operation_id BETWEEN 1 AND 32 AND work_run_id IS NOT NULL
    AND actor_backend IS NOT DISTINCT FROM 'harness' AND harness_proposal IS NOT NULL));
