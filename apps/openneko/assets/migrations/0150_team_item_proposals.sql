-- A run does not write team memory, team skills or skill lessons that
-- other people's runs load. It proposes the change, and an admin confirms
-- or rejects it with the full text and a diff on screen.
create table if not exists team_item_proposal (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  kind text not null check (kind in ('memory', 'skill', 'skill_lesson')),
  target text not null,
  before jsonb,
  after jsonb not null,
  base_hash text not null default '',
  proposed_by_user_id text references app_user(id) on delete set null,
  proposed_by_run_id uuid references work_run(id) on delete set null,
  status text not null default 'pending' check (status in ('pending', 'confirmed', 'rejected', 'stale')),
  decided_by_user_id text references app_user(id) on delete set null,
  decided_at timestamptz,
  created_at timestamptz not null default now()
);

create index if not exists team_item_proposal_org_status_idx
  on team_item_proposal (org_id, status, created_at desc);

-- An admin approves one version of a workflow for API and webhook runs:
-- the hash of its steps, triggers, inputs and the skills its steps use.
alter table workflow_definition
  add column if not exists access_approved_hash text;
