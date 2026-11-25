-- A record of the world builder: the mapping of an organization, or the
-- object moves of one person. An empty user_id marks a record of the
-- whole organization.
create table if not exists world_record (
  org_id text not null references organization(id) on delete cascade,
  user_id text not null default '',
  name text not null check (name ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
  value jsonb not null,
  updated_at timestamptz not null default now(),
  primary key (org_id, user_id, name)
);
