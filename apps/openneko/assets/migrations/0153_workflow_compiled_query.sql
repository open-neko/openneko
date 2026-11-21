-- The compiled query of a workflow: the query that answered a finished run,
-- with its dates as variables. The next run gets its result before the agent
-- starts. A run whose compiled query fails drops it, and the next good run
-- compiles it again.
create table if not exists workflow_compiled_query (
  workflow_id uuid primary key references workflow_definition(id) on delete cascade,
  org_id text not null references organization(id) on delete cascade,
  anchor_query text,
  query text not null,
  variables jsonb not null default '[]'::jsonb,
  source_run_id uuid not null,
  -- The goal the query answers. A changed goal drops the query.
  goal_hash text not null default '',
  uses integer not null default 0,
  created_at timestamptz not null default now(),
  last_used_at timestamptz
);

alter table llm_task_route drop constraint if exists llm_task_route_task_check;
alter table llm_task_route add constraint llm_task_route_task_check check (task in (
  'default', 'turn', 'classify', 'briefing_summary', 'compact',
  'bot_instructions', 'bot_names', 'skill_lessons', 'research_mission', 'workflow_compile'
));
