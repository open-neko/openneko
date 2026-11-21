-- A stored source password goes only to the database and the login that
-- the admin saved it with. A password saved before has neither, and a
-- source that uses it waits until an admin saves it again.
alter table data_source_secret
  add column if not exists dbname text,
  add column if not exists login text;
