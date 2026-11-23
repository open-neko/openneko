-- Tests mined from real threads, and the playbook a verifier keeps.
--
-- A mined test is a question that a run answered with one figure or one top
-- row from a GraphJin query. Its oracle is that query, run again on the same
-- data when the test runs.
-- When a person corrected an answer, the test asks the first question and
-- takes its oracle from the run that answered the correction.
create table if not exists mined_test (
  run_id uuid primary key,
  org_id text not null references organization(id) on delete cascade,
  thread_id uuid not null,
  question text not null,
  context jsonb not null default '[]'::jsonb,
  correction text,
  query text not null,
  value_path jsonb not null,
  value text not null,
  value_kind text not null check (value_kind in ('number', 'name', 'none')),
  -- A run with no test keeps a row with its reason, so it is read once.
  skip_reason text,
  last_pass boolean,
  last_queries integer,
  last_run_at timestamptz,
  created_at timestamptz not null default now()
);

create index if not exists mined_test_org on mined_test (org_id, created_at);

-- A playbook item is a short rule for every run of the org. It becomes
-- active only when replaying mined tests with it beats replaying them
-- without it.
create table if not exists playbook_item (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  body text not null,
  state text not null check (state in ('candidate', 'active', 'rejected')),
  source_run_id uuid,
  evidence jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  decided_at timestamptz
);

create index if not exists playbook_item_org on playbook_item (org_id, state);

alter table llm_task_route drop constraint if exists llm_task_route_task_check;
alter table llm_task_route add constraint llm_task_route_task_check check (task in (
  'default', 'turn', 'classify', 'briefing_summary', 'compact',
  'bot_instructions', 'bot_names', 'skill_lessons', 'research_mission', 'workflow_compile', 'playbook'
));
