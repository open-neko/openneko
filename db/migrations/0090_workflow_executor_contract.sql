-- Queue retries execute the workflow contract admitted with the run, not a
-- later edit to the definition or user-provided trigger payload.
ALTER TABLE workflow_run
  ADD COLUMN executor_contract jsonb,
  ADD CONSTRAINT workflow_run_executor_contract_bound
    CHECK (executor_contract IS NULL OR octet_length(executor_contract::text) <= 16384);
