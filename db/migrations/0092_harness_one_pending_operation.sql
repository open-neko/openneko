-- Pin the host operation budget and block new effects after an unknown result.
ALTER TABLE harness_run_journal ADD COLUMN operation_limit integer NOT NULL DEFAULT 4;
ALTER TABLE harness_run_journal ADD CONSTRAINT harness_run_journal_operation_limit_check
  CHECK (operation_limit BETWEEN 1 AND 32);
CREATE UNIQUE INDEX harness_operation_one_pending
  ON harness_operation (org_id, run_id) WHERE result IS NULL;
