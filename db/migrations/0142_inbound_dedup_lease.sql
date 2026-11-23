-- A pending inbound update belongs to one web process until its lease ends,
-- so two processes on one database do not dispatch the same update.

alter table inbound_dedup add column if not exists claimed_by text;
alter table inbound_dedup add column if not exists claimed_until timestamptz;
