-- The rows that a row-change subscription has handled: one row for each
-- matched source row, with a hash of its content.
create table if not exists subscription_seen (
  subscription_id uuid not null references subscription(id) on delete cascade,
  key_hash text not null,
  row_hash text not null,
  seen_at timestamptz not null default now(),
  primary key (subscription_id, key_hash)
);

-- A row-change subscription that is older than this table records its
-- first result with no run. A new subscription runs for each row.
alter table subscription add column if not exists baseline_pending boolean not null default false;
update subscription set baseline_pending = true where source_kind = 'source_change';
