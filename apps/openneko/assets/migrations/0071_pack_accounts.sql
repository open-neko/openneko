-- Pack OAuth data has its own owner boundary. It is not a source or plugin secret.
create table pack_connection_client (
  pack_install_id uuid not null references pack_install(id) on delete cascade,
  connector_id text not null,
  auth_hash text not null,
  value_enc text not null,
  primary key (pack_install_id, connector_id)
);
create table pack_account (
  id uuid primary key,
  pack_install_id uuid not null,
  connector_id text not null,
  owner_id text not null,
  status text not null check (status in ('pending', 'connected', 'reconnect_required')),
  value_enc text not null,
  label text not null default '',
  updated_at timestamptz not null default now(),
  foreign key (pack_install_id, connector_id) references pack_connection_client(pack_install_id, connector_id) on delete cascade
);
create index pack_account_owner on pack_account(pack_install_id, connector_id, owner_id);
