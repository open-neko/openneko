-- The keyword index of the files in each user's private folder. openneko
-- serve rebuilds it from the files, so it is safe to empty.

create table if not exists file_content_file (
  key text primary key,
  scope text not null,
  id text not null,
  base text not null,
  version text not null,
  kind text,
  error text,
  text text
);
create index if not exists file_content_file_scope_idx on file_content_file (scope);

create table if not exists file_content_unit (
  id bigserial primary key,
  key text not null references file_content_file (key) on delete cascade,
  location text not null,
  body text not null,
  tsv tsvector generated always as (to_tsvector('english', body)) stored
);
create index if not exists file_content_unit_key_idx on file_content_unit (key);
create index if not exists file_content_unit_tsv_idx on file_content_unit using gin (tsv);

create table if not exists file_content_table (
  scope text not null,
  name text not null,
  key text not null references file_content_file (key) on delete cascade,
  ord integer not null,
  file text not null,
  info jsonb not null,
  primary key (scope, name)
);
create index if not exists file_content_table_key_idx on file_content_table (key);
