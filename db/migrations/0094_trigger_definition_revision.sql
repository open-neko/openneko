-- A trigger occurrence must retain the definition revision that admitted it.
-- Existing unstarted occurrences have no trustworthy revision and are fenced
-- by the claim/recovery paths until they can be terminalized.
ALTER TABLE workflow_schedule_firing
  ADD COLUMN definition_updated_at timestamptz;
ALTER TABLE source_change_delivery
  ADD COLUMN definition_updated_at timestamptz;
