-- A run that edits a workflow with API access puts that access on hold
-- until an admin approves the new content.
alter table workflow_definition
  add column if not exists access_held_at timestamptz;

-- A run does not save an approval rule. It proposes one, and an admin
-- confirms or rejects the proposal.
create table if not exists action_policy_proposal (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  name text not null,
  rule jsonb not null,
  proposed_by_user_id text references app_user(id) on delete set null,
  proposed_by_run_id uuid references work_run(id) on delete set null,
  status text not null default 'pending' check (status in ('pending', 'confirmed', 'rejected')),
  decided_by_user_id text references app_user(id) on delete set null,
  decided_at timestamptz,
  policy_id uuid references action_policy(id) on delete set null,
  created_at timestamptz not null default now()
);

create index if not exists action_policy_proposal_org_status_idx
  on action_policy_proposal (org_id, status, created_at desc);
