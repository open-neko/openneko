-- A record per model and a route per task. The org's primary provider row
-- stays the default model; llm_model holds the other models an admin adds.
-- A route with no model uses the default model. Deleting a model that a
-- route names is refused at the end of the statement, so an org delete still cascades.

create table if not exists llm_model (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  name text not null check (name ~ '^[a-z][a-z0-9-]{0,39}$' and name <> 'default'),
  provider text not null,
  model text not null check (length(model) between 1 and 128),
  config jsonb not null default '{}'::jsonb,
  secrets jsonb not null default '{}'::jsonb,
  enabled boolean not null default true,
  input_usd_per_1m numeric(12, 4) check (input_usd_per_1m >= 0),
  output_usd_per_1m numeric(12, 4) check (output_usd_per_1m >= 0),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (org_id, name),
  unique (org_id, id),
  check ((input_usd_per_1m is null) = (output_usd_per_1m is null))
);

create table if not exists llm_task_route (
  org_id text not null references organization(id) on delete cascade,
  task text not null check (task in (
    'default', 'turn', 'classify', 'briefing_summary', 'compact',
    'bot_instructions', 'bot_names', 'skill_lessons', 'research_mission'
  )),
  model_id uuid,
  fallback_model_id uuid,
  updated_at timestamptz not null default now(),
  primary key (org_id, task),
  foreign key (org_id, model_id) references llm_model(org_id, id),
  foreign key (org_id, fallback_model_id) references llm_model(org_id, id),
  check (task <> 'default' or model_id is null),
  check (model_id is null or fallback_model_id is null or model_id <> fallback_model_id)
);

alter table spend_ledger add column if not exists task text;

alter table spend_ledger drop constraint if exists spend_ledger_priced_check;
alter table spend_ledger add constraint spend_ledger_priced_check
  check (priced in ('billed', 'estimated', 'included', 'fallback', 'unpriced'));

create index if not exists spend_ledger_org_task_idx
  on spend_ledger (org_id, created_at, task);
