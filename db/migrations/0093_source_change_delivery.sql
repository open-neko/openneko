-- Source-change stream events need a durable identity before observation and
-- queue dispatch. A pg-boss singleton alone does not deduplicate the
-- observation/audit writes or survive its one-hour window.
CREATE TABLE source_change_delivery (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id text NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscription(id) ON DELETE CASCADE,
  subscription_updated_at timestamptz NOT NULL,
  workflow_id uuid NOT NULL REFERENCES workflow_definition(id) ON DELETE CASCADE,
  source_id uuid NOT NULL REFERENCES data_source(id) ON DELETE CASCADE,
  delivery_key text NOT NULL,
  observation_id uuid REFERENCES observation(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'dispatching', 'enqueued', 'running', 'completed', 'cancelled')),
  queue_job_id text,
  workflow_run_id uuid REFERENCES workflow_run(id) ON DELETE SET NULL,
  trigger_payload jsonb NOT NULL,
  lease_until timestamptz,
  available_at timestamptz NOT NULL DEFAULT now(),
  attempts integer NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT source_change_delivery_unique UNIQUE (org_id, subscription_id, subscription_updated_at, delivery_key)
);
CREATE INDEX source_change_delivery_pending_idx
  ON source_change_delivery (status, available_at);
CREATE UNIQUE INDEX source_change_delivery_workflow_run_unique
  ON source_change_delivery (workflow_run_id) WHERE workflow_run_id IS NOT NULL;
