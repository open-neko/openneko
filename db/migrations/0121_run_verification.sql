-- Human-free correctness signals for the learner: deterministic answer checks
-- on every completed run, and a sampled independent recomputation.

create table if not exists run_verification (
  id uuid primary key default gen_random_uuid(),
  org_id text not null references organization(id) on delete cascade,
  run_id uuid not null references work_run(id) on delete cascade,
  kind text not null check (kind in ('check', 'recompute')),
  check_id text not null,
  verdict text not null check (verdict in ('pending', 'pass', 'fail', 'unknown', 'agree', 'disagree')),
  detail text not null default '',
  data jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint run_verification_run_check_unique unique (run_id, kind, check_id)
);

create index if not exists run_verification_org_kind_recent_idx
  on run_verification (org_id, kind, created_at desc);
