-- A stored source password goes only to the host and port saved with it.
-- Passwords from before this have no host and wait to be saved again.

alter table data_source_secret
  add column if not exists host text,
  add column if not exists port integer,
  add column if not exists allow_unverified_tls boolean not null default false;
