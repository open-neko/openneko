-- A durable claim outlives worker death. Ambiguous effects are never redispatched.
CREATE UNIQUE INDEX IF NOT EXISTS action_execution_harness_claim
ON action_execution(org_id, action_request_id) WHERE executor = 'harness';
