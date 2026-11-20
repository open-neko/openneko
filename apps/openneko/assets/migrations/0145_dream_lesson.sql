-- Dreaming: lessons that the learning pass writes for an org, and the record
-- of each lesson offered to a run.

create table if not exists dream_lesson (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  kind text not null check (kind in ('query', 'schema_fact', 'dead_end', 'business_rule', 'data_fact')),
  question_shape text not null check (length(trim(question_shape)) > 0),
  body text not null check (length(trim(body)) > 0),
  tables text[] not null default '{}',
  catalog_revision text,
  state text not null default 'active' check (state in ('active', 'candidate', 'challenger', 'retired')),
  supersedes_id uuid references dream_lesson(id) on delete set null,
  source_run_ids uuid[] not null default '{}',
  embedding vector,
  embedding_model text,
  use_count integer not null default 0,
  last_used_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index if not exists dream_lesson_org_state_idx
  on dream_lesson (org_id, state);

create table if not exists dream_lesson_use (
  run_id uuid not null references work_run(id) on delete cascade,
  lesson_id uuid not null references dream_lesson(id) on delete cascade,
  org_id text not null references organization(id) on delete cascade,
  outcome text check (outcome in ('succeeded', 'failed_source', 'failed_access', 'corrected')),
  created_at timestamptz not null default now(),
  primary key (run_id, lesson_id)
);

create index if not exists dream_lesson_use_lesson_idx
  on dream_lesson_use (lesson_id);

-- One row per thread that a learning pass read. A thread is read again when
-- it gets a message after last_message_at.
create table if not exists dream_pass (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  thread_id uuid not null references work_thread(id) on delete cascade,
  last_message_at timestamptz not null,
  decisions jsonb not null default '[]',
  error text,
  created_at timestamptz not null default now()
);

create index if not exists dream_pass_thread_idx
  on dream_pass (thread_id, last_message_at desc);

alter table llm_task_route drop constraint if exists llm_task_route_task_check;
alter table llm_task_route add constraint llm_task_route_task_check check (task in (
  'default', 'turn', 'classify', 'briefing_summary', 'compact',
  'bot_instructions', 'bot_names', 'skill_lessons', 'research_mission', 'dreaming'
));
