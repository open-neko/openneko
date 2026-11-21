-- A setting of the whole install, for every organization. Only openneko web
-- writes it; no serve role has a grant on it.
create table if not exists install_setting (
  name text primary key,
  value jsonb not null,
  updated_by_user_id text references app_user(id) on delete set null,
  updated_at timestamptz not null default now()
);

alter table work_run
  add column if not exists ran_unconfined boolean not null default false;
