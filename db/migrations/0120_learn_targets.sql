-- The learner serves typed targets: skill rules, data source notes, skill
-- descriptions and the org playbook. Existing rows are skill targets.

alter table skill_learn_event
  add column if not exists target_kind text not null default 'skill',
  add column if not exists target_id text,
  add column if not exists cohort_digest text;

update skill_learn_event set target_kind = 'discovery'
  where skill_name = '_discovery' and target_kind = 'skill';

update skill_learn_event set target_id = skill_name where target_id is null;

create index if not exists skill_learn_event_org_target_recent_idx
  on skill_learn_event (org_id, target_kind, target_id, created_at desc);

-- The proposer runs at most once per target and evidence cohort.
create unique index if not exists skill_learn_event_cohort_unique
  on skill_learn_event (org_id, target_kind, target_id, cohort_digest)
  where cohort_digest is not null;

alter table skill_learn_state
  add column if not exists target_kind text not null default 'skill',
  add column if not exists target_id text;

update skill_learn_state set target_id = skill_name where target_id is null;

-- Learned rules a run cited, for per-rule credit and rollback.
create table if not exists learned_rule_use (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  run_id uuid not null references work_run(id) on delete cascade,
  rule_key text not null,
  target_kind text not null,
  target_id text not null,
  version text not null,
  created_at timestamptz not null default now(),
  constraint learned_rule_use_run_rule_unique unique (run_id, rule_key)
);

create index if not exists learned_rule_use_org_rule_idx
  on learned_rule_use (org_id, rule_key, created_at desc);
