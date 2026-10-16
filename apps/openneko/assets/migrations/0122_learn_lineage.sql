-- Learning lessons and notable learning events are observations with run
-- lineage. Runs link to a parent and a root task, pin the learned-store
-- versions they read, and record the channel messages they used.

alter table observation
  add column if not exists detail jsonb not null default '{}'::jsonb,
  add column if not exists dedupe_key text;

create unique index if not exists observation_org_dedupe_unique
  on observation (org_id, dedupe_key)
  where dedupe_key is not null;

create index if not exists observation_org_consumer_recent_idx
  on observation (org_id, consumer_kind, created_at desc);

create table if not exists observation_run (
  observation_id uuid not null references observation(id) on delete cascade,
  run_id uuid not null references work_run(id) on delete cascade,
  quote text,
  created_at timestamptz not null default now(),
  primary key (observation_id, run_id)
);

create index if not exists observation_run_run_idx on observation_run (run_id);

alter table work_run
  add column if not exists parent_run_id uuid references work_run(id) on delete set null,
  add column if not exists root_run_id uuid references work_run(id) on delete set null;

create index if not exists work_run_root_idx
  on work_run (root_run_id)
  where root_run_id is not null;

create table if not exists run_store_version (
  org_id text not null references organization(id) on delete cascade,
  run_id uuid not null references work_run(id) on delete cascade,
  target_kind text not null,
  target_id text not null,
  version text not null,
  created_at timestamptz not null default now(),
  primary key (run_id, target_kind, target_id)
);

create table if not exists run_channel_message (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  run_id uuid not null references work_run(id) on delete cascade,
  channel_thread text not null,
  message_ref text not null,
  sender_kind text not null check (sender_kind in ('human', 'agent')),
  sender_id text,
  access_scope text not null default 'private',
  content text not null,
  created_at timestamptz not null default now(),
  constraint run_channel_message_run_message_unique unique (run_id, message_ref)
);
