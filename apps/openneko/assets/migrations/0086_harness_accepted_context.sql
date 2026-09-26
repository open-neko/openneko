-- Preserve accepted model context while checking current request/authorization scope.
ALTER TABLE harness_run_journal ADD COLUMN IF NOT EXISTS accepted_context jsonb;
ALTER TABLE harness_run_journal ADD CONSTRAINT harness_run_journal_context_bounded
  CHECK (accepted_context IS NULL OR octet_length(accepted_context::text) <= 8388608);
